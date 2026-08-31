package rewe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// currencyEUR is hardcoded because REWE only trades in EUR and none of the
// basket/timeslot payloads researched (Tobi4s1337/karrt's BasketSummary,
// LineItem, Timeslot types) carry a currency field of their own — unlike
// ProductPricing.currency, which products_search does observe.
const currencyEUR = "EUR"

// GetBasket implements shopping.BasketGateway. REWE has no "current basket"
// endpoint independent of an id — GET /baskets/{basketId} needs one, and
// there is no basket until something has been added — so ShoppingContext's
// bound BasketID (set by ApplyBasket via WithBasket) is the only source.
func (g Gateway) GetBasket(ctx context.Context, shoppingContext shopping.ShoppingContext) (shopping.Basket, error) {
	if shoppingContext.BasketID == "" {
		result, err := g.Transport.Do(ctx, browserbridge.OperationBasketDiscover, nil)
		if err != nil {
			return shopping.Basket{}, classifyReadBridgeError("discover basket", err)
		}
		if isEmptyJSON(result) {
			return shopping.Basket{}, &shopping.ValidationError{Operation: "get basket", Field: "basket_id", Problem: shopping.ValidationMissing}
		}
		var discovery struct {
			Found  bool            `json:"found"`
			Basket json.RawMessage `json:"basket"`
		}
		if err := json.Unmarshal(result, &discovery); err != nil {
			return shopping.Basket{}, &shopping.UpstreamChangeError{Operation: "discover basket"}
		}
		if !discovery.Found {
			return shopping.Basket{}, &shopping.ValidationError{Operation: "get basket", Field: "basket_id", Problem: shopping.ValidationMissing}
		}
		payload, err := decodeCritical[basketPayload]("basket.discover", unwrapBasketDocument(discovery.Basket))
		if err != nil {
			return shopping.Basket{}, err
		}
		return payload.toBasket(g.now()), nil
	}

	params, err := json.Marshal(basketGetParams{BasketID: string(shoppingContext.BasketID)})
	if err != nil {
		return shopping.Basket{}, fmt.Errorf("encode basket_get params: %w", err)
	}
	result, err := g.Transport.Do(ctx, browserbridge.OperationBasketGet, params)
	if err != nil {
		return shopping.Basket{}, classifyReadBridgeError("get basket", err)
	}

	payload, err := decodeCritical[basketPayload]("basket.get", unwrapBasketDocument(result))
	if err != nil {
		return shopping.Basket{}, err
	}
	return payload.toBasket(g.now()), nil
}

// ApplyBasket implements shopping.BasketGateway. REWE has no bulk basket
// endpoint (confirmed against karrt's src/api/index.ts: basketAdd/
// basketUpdate/basketRemove are each one REWE call), so every change in the
// mutation is its own POST/DELETE, looped inside the content script (see
// extension/content-script.js's handleBasketApply). This method never
// retries: a partial failure across items is reported per-item via
// BasketChangeOutcome, and a transport-level failure whose outcome is
// unknown (e.g. the request context was canceled while awaiting REWE's
// reply) is surfaced as AmbiguousResultError rather than guessed at.
//
// BasketChange.ProductID is sent to the content script as REWE's listingId
// (the identifier /baskets/listings/{listingId} actually takes). karrt's
// own types distinguish ProductId from ListingId, and this project's
// domain model only has ProductID — so this assumes whatever populates
// Product.ID (card #7's products_search gateway) puts REWE's listing id
// there, since that is the only identifier basket operations can use. If
// that assumption is wrong, every apply fails closed (upstream_changed /
// rejected outcomes), it does not silently touch the wrong listing.
func (g Gateway) ApplyBasket(ctx context.Context, shoppingContext shopping.ShoppingContext, mutation shopping.BasketMutation) (shopping.BasketMutationResult, error) {
	if len(mutation.Changes) == 0 {
		return shopping.BasketMutationResult{}, &shopping.ValidationError{Operation: "apply basket", Field: "changes", Problem: shopping.ValidationMissing}
	}

	outcomes := make([]shopping.BasketChangeOutcome, 0, len(mutation.Changes))
	var latestBasket *shopping.Basket
	reconciled := false
	reconciliationProblem := shopping.ReconciliationNone
	now := g.now()
	for index, change := range mutation.Changes {
		params, err := json.Marshal(basketApplyParams{
			BasketID: string(shoppingContext.BasketID),
			Changes:  []basketApplyChangeParams{{ListingID: string(change.ProductID), Quantity: change.Quantity}},
		})
		if err != nil {
			return shopping.BasketMutationResult{}, fmt.Errorf("encode basket_apply params: %w", err)
		}
		result, err := g.Transport.Do(ctx, browserbridge.OperationBasketApply, params)
		if err != nil {
			if len(outcomes) == 0 {
				return shopping.BasketMutationResult{}, classifyMutationBridgeError("apply basket", err)
			}
			for _, remaining := range mutation.Changes[index:] {
				outcomes = append(outcomes, shopping.BasketChangeOutcome{ProductID: remaining.ProductID, RequestedQuantity: remaining.Quantity, Status: shopping.BasketChangeUnknown, Problem: shopping.BasketProblemUnknown})
			}
			reconciliationProblem = reconciliationProblemForError(err)
			reconciled = false
			break
		}
		payload, err := decodeCritical[basketApplyResultPayload]("basket.apply", result)
		if err != nil || len(payload.Changes) != 1 {
			if len(outcomes) == 0 {
				return shopping.BasketMutationResult{}, &shopping.AmbiguousResultError{Operation: "apply basket"}
			}
			for _, remaining := range mutation.Changes[index:] {
				outcomes = append(outcomes, shopping.BasketChangeOutcome{ProductID: remaining.ProductID, RequestedQuantity: remaining.Quantity, Status: shopping.BasketChangeUnknown, Problem: shopping.BasketProblemUnknown})
			}
			reconciliationProblem = shopping.ReconciliationUpstreamChanged
			reconciled = false
			break
		}
		itemResult := payload.Changes[0]
		if itemResult.ListingID != "" && itemResult.ListingID != string(change.ProductID) {
			outcomes = append(outcomes, shopping.BasketChangeOutcome{
				ProductID: change.ProductID, RequestedQuantity: change.Quantity, Status: shopping.BasketChangeUnknown,
				Problem: shopping.BasketProblemUnknown,
			})
			reconciliationProblem = shopping.ReconciliationIncompatibleItemResult
			reconciled = false
			continue
		}

		if !itemResult.OK {
			status := shopping.BasketChangeRejected
			if itemResult.Code == "ambiguous_result" || itemResult.Code == "canceled" || itemResult.Code == "content_script_unreachable" {
				status = shopping.BasketChangeUnknown
				reconciled = false
			}
			if itemResult.Code == "auth_invalid" || itemResult.Code == "rate_limited" {
				reconciliationProblem = safeReconciliationProblem(itemResult.Code)
			}
			outcomes = append(outcomes, shopping.BasketChangeOutcome{
				ProductID:         change.ProductID,
				RequestedQuantity: change.Quantity,
				AppliedQuantity:   0,
				Status:            status,
				Problem:           basketProblem(itemResult.Code),
			})
			continue
		}

		applied := change.Quantity
		status := shopping.BasketChangeApplied
		if snapshotBytes := unwrapBasketDocument(itemResult.Result); !isEmptyJSON(snapshotBytes) {
			snapshot, decodeErr := decodeCritical[basketPayload]("basket.apply", snapshotBytes)
			if decodeErr != nil {
				status = shopping.BasketChangeUnknown
				reconciliationProblem = shopping.ReconciliationIncompatibleItemResult
				reconciled = false
			} else {
				applied = snapshot.appliedQuantity(change.ProductID)
				if applied != change.Quantity {
					status = shopping.BasketChangeAdjusted
				}
				basket := snapshot.toBasket(now)
				latestBasket = &basket
			}
		}
		outcomes = append(outcomes, shopping.BasketChangeOutcome{
			ProductID:         change.ProductID,
			RequestedQuantity: change.Quantity,
			AppliedQuantity:   applied,
			Status:            status,
		})
		if !isEmptyJSON(payload.Basket) {
			snapshot, decodeErr := decodeCritical[basketPayload]("basket.apply.reconcile", unwrapBasketDocument(payload.Basket))
			if decodeErr != nil {
				reconciliationProblem = shopping.ReconciliationUpstreamChanged
				reconciled = false
			} else {
				basket := snapshot.toBasket(now)
				latestBasket = &basket
				reconciled = true
				reconciliationProblem = shopping.ReconciliationNone
				shoppingContext = shoppingContext.WithBasket(basket.ID)
			}
		} else if payload.ReconciliationCode != "" {
			reconciliationProblem = safeReconciliationProblem(payload.ReconciliationCode)
			reconciled = false
		}
	}

	// A DELETE response has no body (204), so a remove-only or remove-last
	// mutation never populates latestBasket from an inline snapshot. Fetch
	// the authoritative post-mutation state explicitly rather than reporting
	// a zero-value Basket — card #8's own AC requires "the authoritative
	// updated basket" on every apply, not only ones that happened to add.
	if !reconciled && shoppingContext.BasketID != "" {
		refreshed, err := g.GetBasket(ctx, shoppingContext)
		if err != nil {
			if reconciliationProblem == shopping.ReconciliationNone {
				reconciliationProblem = reconciliationProblemForError(err)
			}
		} else {
			latestBasket = &refreshed
			reconciled = true
			reconciliationProblem = shopping.ReconciliationNone
		}
	}
	basket := shopping.Basket{}
	if latestBasket != nil {
		basket = *latestBasket
	}
	if reconciled {
		for index := range outcomes {
			applied := basketQuantity(basket, outcomes[index].ProductID)
			outcomes[index].AppliedQuantity = applied
			if outcomes[index].Status != shopping.BasketChangeRejected {
				if applied == outcomes[index].RequestedQuantity {
					outcomes[index].Status = shopping.BasketChangeApplied
				} else {
					outcomes[index].Status = shopping.BasketChangeAdjusted
				}
			}
		}
	}
	if !reconciled && reconciliationProblem == shopping.ReconciliationNone {
		reconciliationProblem = shopping.ReconciliationBasketIDUnknown
	}
	return shopping.BasketMutationResult{
		Basket: basket, Outcomes: outcomes, Reconciled: reconciled, ReconciliationProblem: reconciliationProblem,
	}, nil
}

func basketProblem(code string) shopping.BasketChangeProblem {
	switch code {
	case "auth_invalid":
		return shopping.BasketProblemAuthInvalid
	case "rate_limited":
		return shopping.BasketProblemRateLimited
	case "invalid_params":
		return shopping.BasketProblemInvalid
	case "upstream_changed", "malformed_response":
		return shopping.BasketProblemUpstream
	default:
		return shopping.BasketProblemUnknown
	}
}

func safeReconciliationProblem(code string) shopping.ReconciliationProblem {
	switch code {
	case "auth_invalid":
		return shopping.ReconciliationAuthInvalid
	case "rate_limited":
		return shopping.ReconciliationRateLimited
	case "upstream_changed", "malformed_response":
		return shopping.ReconciliationUpstreamChanged
	case "ambiguous_result", "canceled", "content_script_unreachable", "bridge_unavailable":
		return shopping.ReconciliationBridgeInterrupted
	default:
		return shopping.ReconciliationBasketUnavailable
	}
}

func reconciliationProblemForError(err error) shopping.ReconciliationProblem {
	var authErr *shopping.AuthError
	if errors.As(err, &authErr) {
		return shopping.ReconciliationAuthInvalid
	}
	var rateErr *shopping.RateLimitError
	if errors.As(err, &rateErr) {
		return shopping.ReconciliationRateLimited
	}
	var upstreamErr *shopping.UpstreamChangeError
	if errors.As(err, &upstreamErr) {
		return shopping.ReconciliationUpstreamChanged
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		return safeReconciliationProblem(coded.Code())
	}
	return shopping.ReconciliationBridgeInterrupted
}

func basketQuantity(basket shopping.Basket, productID shopping.ProductID) int {
	for _, item := range basket.Items {
		if item.ProductID == productID {
			return item.Quantity
		}
	}
	return 0
}

// timeslotsListParams matches content-script.js's storeContextHeaders,
// which builds REWE's required rd-market-id/rd-postcode request headers
// from these fields — karrt's own reference client sends the same pair on
// every timeslots call (src/api/index.ts, storeHeaders), confirmed against
type timeslotsListParams struct {
	MarketID   string `json:"market_id"`
	PostalCode string `json:"postal_code"`
}

type timeslotReserveParams struct {
	SlotID shopping.TimeSlotID `json:"slot_id"`
}

// ListTimeSlots implements shopping.BasketGateway via GET
// /shop/api/timeslots/pickup/overview. REWE can't return pickup slots
// without knowing which market, so this fails closed rather than reaching
// out with an empty market ID.
func (g Gateway) ListTimeSlots(ctx context.Context, shoppingContext shopping.ShoppingContext) (shopping.TimeSlotList, error) {
	if shoppingContext.StoreID == "" || shoppingContext.PostalCode == "" {
		return shopping.TimeSlotList{}, &shopping.ValidationError{Operation: "list timeslots", Field: "store_id", Problem: shopping.ValidationMissing}
	}
	params, err := json.Marshal(timeslotsListParams{MarketID: string(shoppingContext.StoreID), PostalCode: shoppingContext.PostalCode})
	if err != nil {
		return shopping.TimeSlotList{}, err
	}
	result, err := g.Transport.Do(ctx, browserbridge.OperationTimeslotsList, params)
	if err != nil {
		return shopping.TimeSlotList{}, classifyReadBridgeError("list timeslots", err)
	}
	now := g.now()
	slots, err := decodeTimeSlots("timeslots.list", shoppingContext.StoreID, now, result)
	if err != nil {
		return shopping.TimeSlotList{}, err
	}
	return shopping.TimeSlotList{TimeSlots: slots, ObservedAt: now.UTC()}, nil
}

// SelectTimeSlot performs at most one reservation call and reconciles its
// outcome through the timeslot overview. Live checkout research confirmed
// that REWE expects only {slotId}; browser session context supplies the rest.
func (g Gateway) SelectTimeSlot(ctx context.Context, current shopping.ShoppingContext, id shopping.TimeSlotID) (shopping.ShoppingContext, error) {
	if id == "" {
		return shopping.ShoppingContext{}, &shopping.ValidationError{Operation: "select timeslot", Field: "timeslot_id", Problem: shopping.ValidationMissing}
	}
	if current.StoreID == "" || current.PostalCode == "" {
		return shopping.ShoppingContext{}, &shopping.ValidationError{Operation: "select timeslot", Field: "store_id", Problem: shopping.ValidationMissing}
	}

	before, err := g.ListTimeSlots(ctx, current)
	if err != nil {
		return shopping.ShoppingContext{}, err
	}
	found := false
	for _, slot := range before.TimeSlots {
		if slot.ID != id {
			continue
		}
		found = true
		if slot.Selected {
			return current.WithTimeSlot(id), nil
		}
		if !slot.Available {
			return shopping.ShoppingContext{}, &shopping.ValidationError{Operation: "select timeslot", Field: "timeslot_id", Problem: shopping.ValidationConflict}
		}
	}
	if !found {
		return shopping.ShoppingContext{}, &shopping.ValidationError{Operation: "select timeslot", Field: "timeslot_id", Problem: shopping.ValidationInvalid}
	}

	params, err := json.Marshal(timeslotReserveParams{SlotID: id})
	if err != nil {
		return shopping.ShoppingContext{}, fmt.Errorf("encode timeslot reservation: %w", err)
	}
	_, reservationErr := g.Transport.Do(ctx, browserbridge.OperationTimeslotReserve, params)
	after, reconciliationErr := g.ListTimeSlots(ctx, current)
	if reconciliationErr != nil {
		if reservationErr != nil {
			classified := classifyMutationBridgeError("select timeslot", reservationErr)
			var ambiguousErr *shopping.AmbiguousResultError
			if !errors.As(classified, &ambiguousErr) {
				return shopping.ShoppingContext{}, classified
			}
		}
		return shopping.ShoppingContext{}, &shopping.AmbiguousResultError{Operation: "select timeslot"}
	}
	for _, slot := range after.TimeSlots {
		if slot.ID == id && slot.Selected {
			return current.WithTimeSlot(id), nil
		}
	}
	if reservationErr != nil {
		classified := classifyMutationBridgeError("select timeslot", reservationErr)
		var ambiguousErr *shopping.AmbiguousResultError
		if !errors.As(classified, &ambiguousErr) {
			return shopping.ShoppingContext{}, classified
		}
	}
	return shopping.ShoppingContext{}, &shopping.ValidationError{Operation: "select timeslot", Field: "timeslot_id", Problem: shopping.ValidationConflict}
}

type basketGetParams struct {
	BasketID string `json:"basket_id"`
}

type basketApplyChangeParams struct {
	ListingID string `json:"listing_id"`
	Quantity  int    `json:"quantity"`
}

type basketApplyParams struct {
	BasketID string                    `json:"basket_id"`
	Changes  []basketApplyChangeParams `json:"changes"`
}

// classifyReadBridgeError and classifyMutationBridgeError are defined in
// bridge_errors.go, shared with the other two verticals.

// unwrapBasketDocument defends against REWE's basket response being either
// {"basket": {...}} or the bare basket object — the same ambiguity karrt's
// own extractBasket() (`res.basket ?? res`) hedges against, and the same
// class of mistake decodeFavoriteListID's old {favoriteLists:{favorites}}
// assumption turned out to be (card #13). A malformed or non-object body is
// passed through unchanged so decodeCritical still classifies it.
func unwrapBasketDocument(body []byte) []byte {
	var wrapper struct {
		Basket json.RawMessage `json:"basket"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return body
	}
	trimmed := bytes.TrimSpace(wrapper.Basket)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return body
	}
	return wrapper.Basket
}

func isEmptyJSON(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// basketPayload is REWE's basket-v2 document, confirmed live (2026-08-19)
// against a real POST /shop/api/baskets/listings response — the shape
// originally guessed from Tobi4s1337/karrt's Basket type got the top-level
// fields right but the nested product identifier wrong (see
// basketProductPayload). Only id/lineItems/summary are load-bearing,
// everything else (serviceSelection, per-item product title) is best-effort.
type basketPayload struct {
	ID               string                        `json:"id"`
	ServiceSelection basketServiceSelectionPayload `json:"serviceSelection"`
	LineItems        []basketLineItemPayload       `json:"lineItems"`
	Summary          basketSummaryPayload          `json:"summary"`
}

func (basketPayload) criticalFields() []string { return []string{"id", "lineItems", "summary"} }

func (p basketPayload) validate() shopping.UpstreamProblem {
	if strings.TrimSpace(p.ID) == "" {
		return shopping.UpstreamMissingCriticalField
	}
	for _, item := range p.LineItems {
		if problem := item.validate(); problem != "" {
			return problem
		}
	}
	return ""
}

func (p basketPayload) appliedQuantity(productID shopping.ProductID) int {
	for _, item := range p.LineItems {
		if item.Product.Listing.ListingID == string(productID) {
			return item.Quantity
		}
	}
	return 0
}

func (p basketPayload) toBasket(now time.Time) shopping.Basket {
	items := make([]shopping.BasketItem, 0, len(p.LineItems))
	for _, item := range p.LineItems {
		items = append(items, shopping.BasketItem{
			ProductID: shopping.ProductID(item.Product.Listing.ListingID),
			Name:      item.Product.Title,
			Quantity:  item.Quantity,
			UnitPrice: shopping.Money{Cents: item.Price, Currency: currencyEUR},
			LineTotal: shopping.Money{Cents: item.TotalPrice, Currency: currencyEUR},
		})
	}
	fees := p.Summary.Fees.SubstitutesSurcharge + p.Summary.Fees.TransportBoxSurcharge
	return shopping.Basket{
		ID:         shopping.BasketID(p.ID),
		StoreID:    shopping.StoreID(p.ServiceSelection.WwIdent),
		Items:      items,
		Subtotal:   shopping.Money{Cents: p.Summary.ArticlePrice, Currency: currencyEUR},
		Fees:       shopping.Money{Cents: fees, Currency: currencyEUR},
		Total:      shopping.Money{Cents: p.Summary.TotalPrice, Currency: currencyEUR},
		ObservedAt: now.UTC(),
	}
}

type basketServiceSelectionPayload struct {
	WwIdent string `json:"wwIdent"`
}

type basketLineItemPayload struct {
	Quantity   int                  `json:"quantity"`
	Price      int64                `json:"price"`
	TotalPrice int64                `json:"totalPrice"`
	Product    basketProductPayload `json:"product"`
}

func (i basketLineItemPayload) validate() shopping.UpstreamProblem {
	if strings.TrimSpace(i.Product.Listing.ListingID) == "" {
		return shopping.UpstreamMissingCriticalField
	}
	return ""
}

// basketProductPayload's basket-relevant identifier lives at
// product.listing.listingId, not a flat product.id or .productName —
// confirmed live (2026-08-19) against a real POST /shop/api/baskets/listings
// response, which has no top-level "id"/"productName" keys at all (that
// guess, like the rest of this file's pre-live-test shape, came from
// Tobi4s1337/karrt's Basket type and never matched real traffic). The same
// listingId basket_apply already sends as ListingID is what's echoed back
// here, so appliedQuantity/toBasket can match request to response by it.
type basketProductPayload struct {
	Title   string                      `json:"title"`
	Listing basketProductListingPayload `json:"listing"`
}

type basketProductListingPayload struct {
	ListingID string `json:"listingId"`
}

// basketSummaryPayload.Fees, unlike the rest of this struct, was previously
// derived as (total - subtotal) for lack of any confirmed fee field; REWE's
// real summary carries an explicit "fees" object instead, decoded directly
// now that a live response has confirmed its shape.
type basketSummaryPayload struct {
	ArticleCount int               `json:"articleCount"`
	ArticlePrice int64             `json:"articlePrice"`
	TotalPrice   int64             `json:"totalPrice"`
	Fees         basketFeesPayload `json:"fees"`
}

type basketFeesPayload struct {
	SubstitutesSurcharge  int64 `json:"substitutesSurcharge"`
	TransportBoxSurcharge int64 `json:"transportBoxSurcharge"`
}

// basketApplyResultPayload is content-script.js's own aggregate envelope
// (not a REWE shape) — handleBasketApply loops one REWE call per change and
// reports every outcome back in request order.
type basketApplyResultPayload struct {
	Changes            []basketApplyChangeResultPayload `json:"changes"`
	Basket             json.RawMessage                  `json:"basket"`
	ReconciliationCode string                           `json:"reconciliation_code"`
}

func (basketApplyResultPayload) criticalFields() []string { return []string{"changes"} }

func (p basketApplyResultPayload) validate() shopping.UpstreamProblem {
	if p.Changes == nil {
		return shopping.UpstreamTypeChanged
	}
	return ""
}

type basketApplyChangeResultPayload struct {
	ListingID string          `json:"listing_id"`
	OK        bool            `json:"ok"`
	Code      string          `json:"code"`
	Result    json.RawMessage `json:"result"`
}

// timeSlotPayload is REWE's Timeslot shape (karrt's Timeslot type: id,
// startTime, endTime, serviceFee — no availability flag). "available" is
// decoded defensively should REWE include one; absent, every slot the
// overview lists is assumed offered, since nothing in the researched shape
// says otherwise.
type timeSlotPayload struct {
	ID         string `json:"id"`
	StartTime  string `json:"startTime"`
	EndTime    string `json:"endTime"`
	ServiceFee int64  `json:"serviceFee"`
	Available  *bool  `json:"available"`
	Selected   bool   `json:"selected"`
}

func (p timeSlotPayload) toTimeSlot(storeID shopping.StoreID, now time.Time) (shopping.TimeSlot, shopping.UpstreamProblem) {
	if strings.TrimSpace(p.ID) == "" {
		return shopping.TimeSlot{}, shopping.UpstreamMissingCriticalField
	}
	startsAt, err := time.Parse(time.RFC3339, p.StartTime)
	if err != nil {
		return shopping.TimeSlot{}, shopping.UpstreamTypeChanged
	}
	endsAt, err := time.Parse(time.RFC3339, p.EndTime)
	if err != nil {
		return shopping.TimeSlot{}, shopping.UpstreamTypeChanged
	}
	available := true
	if p.Available != nil {
		available = *p.Available
	}
	return shopping.TimeSlot{
		ID:         shopping.TimeSlotID(p.ID),
		StoreID:    storeID,
		StartsAt:   startsAt,
		EndsAt:     endsAt,
		Fee:        shopping.Money{Cents: p.ServiceFee, Currency: currencyEUR},
		Available:  available,
		Selected:   p.Selected,
		ObservedAt: now.UTC(),
	}, ""
}

// decodeTimeSlots hand-rolls the same malformed/type-changed/trailing
// classification decodeCritical gives single objects, but for a top-level
// list. GET /shop/api/timeslots/pickup/overview returns a bare array —
// confirmed live (2026-08-19, 140 real slots) — with two
// extra "labels" field this doesn't decode and no "available" field at all
// despite this struct still
// tolerating one defensively. The {"timeSlots": [...]} wrapper fallback
// below was the untested alternate hypothesis; kept since a real response
// could still take that shape for a different account/market state.
func decodeTimeSlots(operation string, storeID shopping.StoreID, now time.Time, body []byte) ([]shopping.TimeSlot, error) {
	arrayBody, err := unwrapTimeSlotDocuments(operation, body)
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(arrayBody))
	decoder.UseNumber()
	var rawSlots []json.RawMessage
	if err := decoder.Decode(&rawSlots); err != nil {
		return nil, &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamTypeChanged}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamTrailingPayload}
	}

	slots := make([]shopping.TimeSlot, 0, len(rawSlots))
	for _, raw := range rawSlots {
		var item timeSlotPayload
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamTypeChanged}
		}
		slot, problem := item.toTimeSlot(storeID, now)
		if problem != "" {
			return nil, &shopping.UpstreamChangeError{Operation: operation, Problem: problem}
		}
		slots = append(slots, slot)
	}
	return slots, nil
}

func unwrapTimeSlotDocuments(operation string, body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamMalformedJSON}
	}
	if trimmed[0] == '[' {
		return trimmed, nil
	}
	var wrapper struct {
		TimeSlots json.RawMessage `json:"timeSlots"`
	}
	if err := json.Unmarshal(trimmed, &wrapper); err != nil {
		return nil, &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamMalformedJSON}
	}
	if isEmptyJSON(wrapper.TimeSlots) {
		return nil, &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamTypeChanged}
	}
	return wrapper.TimeSlots, nil
}

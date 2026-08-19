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
		return shopping.Basket{}, &shopping.ValidationError{Operation: "get basket", Field: "basket_id", Problem: shopping.ValidationMissing}
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

	wireChanges := make([]basketApplyChangeParams, 0, len(mutation.Changes))
	for _, change := range mutation.Changes {
		wireChanges = append(wireChanges, basketApplyChangeParams{ListingID: string(change.ProductID), Quantity: change.Quantity})
	}
	params, err := json.Marshal(basketApplyParams{BasketID: string(shoppingContext.BasketID), Changes: wireChanges})
	if err != nil {
		return shopping.BasketMutationResult{}, fmt.Errorf("encode basket_apply params: %w", err)
	}

	result, err := g.Transport.Do(ctx, browserbridge.OperationBasketApply, params)
	if err != nil {
		return shopping.BasketMutationResult{}, classifyMutationBridgeError("apply basket", err)
	}

	payload, err := decodeCritical[basketApplyResultPayload]("basket.apply", result)
	if err != nil {
		return shopping.BasketMutationResult{}, err
	}
	if len(payload.Changes) != len(mutation.Changes) {
		return shopping.BasketMutationResult{}, &shopping.UpstreamChangeError{Operation: "basket.apply", Problem: shopping.UpstreamIncompatiblePayload}
	}

	outcomes := make([]shopping.BasketChangeOutcome, 0, len(mutation.Changes))
	var latestBasket *shopping.Basket
	now := g.now()
	for index, change := range mutation.Changes {
		itemResult := payload.Changes[index]
		if itemResult.ListingID != "" && itemResult.ListingID != string(change.ProductID) {
			return shopping.BasketMutationResult{}, &shopping.UpstreamChangeError{Operation: "basket.apply", Problem: shopping.UpstreamIncompatiblePayload}
		}

		if !itemResult.OK {
			switch itemResult.Code {
			case "auth_invalid":
				return shopping.BasketMutationResult{}, &shopping.AuthError{Operation: "apply basket"}
			case "rate_limited":
				return shopping.BasketMutationResult{}, &shopping.RateLimitError{Operation: "apply basket"}
			}
			outcomes = append(outcomes, shopping.BasketChangeOutcome{
				ProductID:         change.ProductID,
				RequestedQuantity: change.Quantity,
				AppliedQuantity:   0,
				Status:            shopping.BasketChangeRejected,
				Problem:           shopping.BasketProblemUnknown,
			})
			continue
		}

		applied := change.Quantity
		status := shopping.BasketChangeApplied
		if snapshotBytes := unwrapBasketDocument(itemResult.Result); !isEmptyJSON(snapshotBytes) {
			snapshot, decodeErr := decodeCritical[basketPayload]("basket.apply", snapshotBytes)
			if decodeErr != nil {
				return shopping.BasketMutationResult{}, decodeErr
			}
			applied = snapshot.appliedQuantity(change.ProductID)
			if applied != change.Quantity {
				status = shopping.BasketChangeAdjusted
			}
			basket := snapshot.toBasket(now)
			latestBasket = &basket
		}
		outcomes = append(outcomes, shopping.BasketChangeOutcome{
			ProductID:         change.ProductID,
			RequestedQuantity: change.Quantity,
			AppliedQuantity:   applied,
			Status:            status,
		})
	}

	// A DELETE response has no body (204), so a remove-only or remove-last
	// mutation never populates latestBasket from an inline snapshot. Fetch
	// the authoritative post-mutation state explicitly rather than reporting
	// a zero-value Basket — card #8's own AC requires "the authoritative
	// updated basket" on every apply, not only ones that happened to add.
	if latestBasket == nil && shoppingContext.BasketID != "" {
		refreshed, err := g.GetBasket(ctx, shoppingContext)
		if err != nil {
			return shopping.BasketMutationResult{}, err
		}
		latestBasket = &refreshed
	}
	basket := shopping.Basket{}
	if latestBasket != nil {
		basket = *latestBasket
	}
	return shopping.BasketMutationResult{Basket: basket, Outcomes: outcomes}, nil
}

// timeslotsListParams matches content-script.js's storeContextHeaders,
// which builds REWE's required rd-market-id/rd-postcode request headers
// from these fields — karrt's own reference client sends the same pair on
// every timeslots call (src/api/index.ts, storeHeaders), confirmed against
// its source directly.
type timeslotsListParams struct {
	MarketID   string `json:"market_id"`
	PostalCode string `json:"postal_code"`
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

// SelectTimeSlot implements shopping.BasketGateway. REWE's POST
// /timeslot-reservations requires a customerId. karrt's own reference
// implementation (Tobi4s1337/karrt, src/api/index.ts, timeslotReserve)
// sends an empty string with the comment "will be populated from session"
// and never actually resolves it — even the project this codebase takes
// its endpoint research from ships that gap. Nothing in ShoppingContext or
// SessionIdentity carries a REWE customer id, and this project's own
// AGENTS.md rules out guessing at values that could corrupt a real
// reservation. Per the frozen contract, this fails closed instead of
// guessing; resolving it needs a live round-trip REWE actually accepts.
func (g Gateway) SelectTimeSlot(_ context.Context, _ shopping.ShoppingContext, _ shopping.TimeSlotID) (shopping.ShoppingContext, error) {
	return shopping.ShoppingContext{}, &shopping.ValidationError{
		Operation: "select timeslot",
		Field:     "customer_id",
		Problem:   shopping.ValidationMissing,
	}
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

// basketPayload is REWE's basket-v2 document (Tobi4s1337/karrt's Basket
// type), decoded defensively: only id/lineItems/summary are load-bearing,
// everything else (serviceSelection, per-item product name) is best-effort.
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
		if item.Product.ID == string(productID) {
			return item.Quantity
		}
	}
	return 0
}

func (p basketPayload) toBasket(now time.Time) shopping.Basket {
	items := make([]shopping.BasketItem, 0, len(p.LineItems))
	for _, item := range p.LineItems {
		items = append(items, shopping.BasketItem{
			ProductID: shopping.ProductID(item.Product.ID),
			Name:      item.Product.Name,
			Quantity:  item.Quantity,
			UnitPrice: shopping.Money{Cents: item.Price, Currency: currencyEUR},
			LineTotal: shopping.Money{Cents: item.TotalPrice, Currency: currencyEUR},
		})
	}
	subtotal := p.Summary.ArticlePrice
	total := p.Summary.TotalPrice
	return shopping.Basket{
		ID:       shopping.BasketID(p.ID),
		StoreID:  shopping.StoreID(p.ServiceSelection.WwIdent),
		Items:    items,
		Subtotal: shopping.Money{Cents: subtotal, Currency: currencyEUR},
		// Fees is derived (total - subtotal): REWE's basket-v2 summary
		// carries no independent fee field per karrt's BasketSummary type,
		// so this is the only self-consistent way to surface it without
		// inventing a field name no evidence supports.
		Fees:       shopping.Money{Cents: total - subtotal, Currency: currencyEUR},
		Total:      shopping.Money{Cents: total, Currency: currencyEUR},
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
	if strings.TrimSpace(i.Product.ID) == "" {
		return shopping.UpstreamMissingCriticalField
	}
	return ""
}

type basketProductPayload struct {
	ID   string `json:"id"`
	Name string `json:"productName"`
}

type basketSummaryPayload struct {
	ArticleCount int   `json:"articleCount"`
	ArticlePrice int64 `json:"articlePrice"`
	TotalPrice   int64 `json:"totalPrice"`
}

// basketApplyResultPayload is content-script.js's own aggregate envelope
// (not a REWE shape) — handleBasketApply loops one REWE call per change and
// reports every outcome back in request order.
type basketApplyResultPayload struct {
	Changes []basketApplyChangeResultPayload `json:"changes"`
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
		ObservedAt: now.UTC(),
	}, ""
}

// decodeTimeSlots hand-rolls the same malformed/type-changed/trailing
// classification decodeCritical gives single objects, but for a top-level
// list. GET /shop/api/timeslots/pickup/overview returns a bare array —
// confirmed live (2026-08-19, 140 real slots) — with two
// extra fields ("labels", "selected") this doesn't decode and silently
// drops, and no "available" field at all despite this struct still
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

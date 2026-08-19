package rewe

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// postalCodePattern accepts a German postal code only — REWE Pickup does not
// operate outside Germany.
var postalCodePattern = regexp.MustCompile(`^[0-9]{5}$`)

// storeIDPattern accepts a REWE market ID (wwIdent): observed live values
// are 6-7 digit numeric strings.
var storeIDPattern = regexp.MustCompile(`^[0-9]{4,10}$`)

const maxProductsPerPage = 50

// storesSearchParams is the JSON sent to content-script.js's
// handleStoresSearch.
type storesSearchParams struct {
	PostalCode string `json:"postal_code"`
}

// storesSearchPayload is what handleStoresSearch returns after mapping
// REWE's real store-locator response (GET
// /api/marketselection/zipcodes/{zip}/services/pickup) — discovered live
// (2026-08-19) by capturing REWE's own "change market" flow directly, not
// guessed at. Replaces an earlier heuristic (reusing /products search plus
// a listing-ID suffix regex, an unverified Tobi4s1337/karrt technique) that
// was confirmed broken: it never found a real market ID, even in
// dense-coverage areas.
type storesSearchPayload struct {
	Stores []storesSearchStore `json:"stores"`
}

// Street-level address is deliberately never decoded here — the contract
// fixture sanitizer treats "street"/"address" as sensitive fields (AGENTS.md:
// "never commit... addresses"), even though REWE's response does include one.
type storesSearchStore struct {
	MarketID       string  `json:"market_id"`
	Name           string  `json:"name"`
	PostalCode     string  `json:"postal_code"`
	City           string  `json:"city"`
	DistanceMeters float64 `json:"distance_meters"`
}

func (storesSearchPayload) criticalFields() []string { return []string{"stores"} }

func (p storesSearchPayload) validate() shopping.UpstreamProblem {
	for _, store := range p.Stores {
		if strings.TrimSpace(store.MarketID) == "" {
			return shopping.UpstreamMissingCriticalField
		}
	}
	return ""
}

// SearchStores discovers real REWE markets serving a postal code, ordered
// by REWE's own reported distance (nearest first).
func (g Gateway) SearchStores(ctx context.Context, _ shopping.ShoppingContext, search shopping.StoreSearch) (shopping.StorePage, error) {
	postalCode := strings.TrimSpace(search.PostalCode)
	if postalCode == "" {
		return shopping.StorePage{}, &shopping.ValidationError{Operation: "stores.search", Field: "postal_code", Problem: shopping.ValidationMissing}
	}
	if !postalCodePattern.MatchString(postalCode) {
		return shopping.StorePage{}, &shopping.ValidationError{Operation: "stores.search", Field: "postal_code", Problem: shopping.ValidationInvalid}
	}

	params, err := json.Marshal(storesSearchParams{PostalCode: postalCode})
	if err != nil {
		return shopping.StorePage{}, err
	}

	result, err := g.Transport.Do(ctx, browserbridge.OperationStoresSearch, params)
	if err != nil {
		return shopping.StorePage{}, classifyReadBridgeError("stores.search", err)
	}

	payload, err := decodeCritical[storesSearchPayload]("stores.search", result)
	if err != nil {
		return shopping.StorePage{}, err
	}

	hasMore := false
	entries := payload.Stores
	if search.Limit > 0 && len(entries) > search.Limit {
		entries = entries[:search.Limit]
		hasMore = true
	}

	observedAt := g.now()
	stores := make([]shopping.Store, 0, len(entries))
	for _, entry := range entries {
		stores = append(stores, shopping.Store{
			ID:             shopping.StoreID(entry.MarketID),
			Name:           entry.Name,
			Address:        shopping.StoreAddress{PostalCode: entry.PostalCode, City: entry.City},
			Pickup:         true,
			DistanceMeters: entry.DistanceMeters,
			ObservedAt:     observedAt,
		})
	}
	return shopping.StorePage{Stores: stores, HasMore: hasMore, ObservedAt: observedAt}, nil
}

// SelectStore needs no REWE call at all: REWE's product and basket calls
// take the market ID as a request parameter each time rather than as
// server-side session state, so this is purely a local context rebind (see
// the frozen contract). It still fails closed on an implausible ID rather
// than binding garbage a later gateway call would have to trust blindly.
func (g Gateway) SelectStore(_ context.Context, current shopping.ShoppingContext, id shopping.StoreID) (shopping.ShoppingContext, error) {
	trimmed := strings.TrimSpace(string(id))
	if trimmed == "" {
		return shopping.ShoppingContext{}, &shopping.ValidationError{Operation: "stores.select", Field: "store_id", Problem: shopping.ValidationMissing}
	}
	if !storeIDPattern.MatchString(trimmed) {
		return shopping.ShoppingContext{}, &shopping.ValidationError{Operation: "stores.select", Field: "store_id", Problem: shopping.ValidationInvalid}
	}
	return current.WithStore(shopping.StoreID(trimmed)), nil
}

// productsSearchParams matches content-script.js's validatedSearchParams
// field names exactly (term/market_id/objects_per_page) — handleProductsSearch
// is already live-proven and is not this vertical's to change.
type productsSearchParams struct {
	Term           string `json:"term"`
	MarketID       string `json:"market_id"`
	ObjectsPerPage int    `json:"objects_per_page,omitempty"`
}

// productsSearchPayload models GET /shop/api/products's real response shape
// — {"hits": [...], "pagination": {...}}, captured directly from a live
// rewe.de search request in DevTools (2026-08-19), not the _embedded.products
// HAL shape this originally guessed (which silently decoded to zero products
// against the real endpoint: "_embedded" was absent, not just differently
// shaped). Every field decodeCritical/validate treats as critical is checked
// explicitly so a wrong assumption here fails with a distinct
// UpstreamChangeError.Problem instead of silently returning zero-valued
// products.
type productsSearchPayload struct {
	Hits       []productsSearchHit       `json:"hits"`
	Pagination *productsSearchPagination `json:"pagination"`
}

// productsSearchHit's listingId (not productId, and not the "id" field the
// original guess used) is REWE's basket-relevant identifier — confirmed by
// the same live capture: basket_apply needs exactly this value as its
// listingId. REWE's search hits carry no availability field at all (no
// "available"/"isBuyable" key anywhere in a live response), so Available is
// no longer decoded from the wire; every hit search returns is presumed
// orderable.
type productsSearchHit struct {
	ListingID string                 `json:"listingId"`
	Title     string                 `json:"title"`
	Pricing   *productsSearchPricing `json:"pricing"`
}

// productsSearchPricing has no currency field on the wire — REWE only
// trades in EUR (see currencyEUR in gateway_basket.go). CurrentRetailPrice's
// unit (cents vs. a decimal euro amount) is unconfirmed from shape alone
// (this project never prints real numeric values); treated as cents per
// karrt's CentPrice type alias, consistent with every other money field
// this codebase decodes.
type productsSearchPricing struct {
	CurrentRetailPrice *int64 `json:"currentRetailPrice"`
	Grammage           string `json:"grammage"`
}

// productsSearchPagination is best-effort: unlike a hit's own critical
// fields, a missing or absent pagination object degrades to HasMore=false
// rather than failing the whole decode.
type productsSearchPagination struct {
	CurrentPage int `json:"currentPage"`
	PageCount   int `json:"pageCount"`
}

func (productsSearchPayload) criticalFields() []string { return []string{"hits"} }

func (p productsSearchPayload) validate() shopping.UpstreamProblem {
	for _, hit := range p.Hits {
		if strings.TrimSpace(hit.ListingID) == "" {
			return shopping.UpstreamMissingCriticalField
		}
		if hit.Pricing == nil || hit.Pricing.CurrentRetailPrice == nil {
			return shopping.UpstreamMissingCriticalField
		}
	}
	return ""
}

// SearchProducts is the Go-side gateway for the already live-proven
// products_search operation (card #13); handleProductsSearch in
// content-script.js is not this vertical's to change. The market to search
// comes from the bound ShoppingContext (set by SelectStore), not from
// ProductSearch itself — REWE scopes every product call to a market ID.
func (g Gateway) SearchProducts(ctx context.Context, current shopping.ShoppingContext, search shopping.ProductSearch) (shopping.ProductPage, error) {
	term := strings.TrimSpace(search.Query)
	if term == "" {
		return shopping.ProductPage{}, &shopping.ValidationError{Operation: "products.search", Field: "query", Problem: shopping.ValidationMissing}
	}
	if current.StoreID == "" {
		return shopping.ProductPage{}, &shopping.ValidationError{Operation: "products.search", Field: "store_id", Problem: shopping.ValidationMissing}
	}
	if search.Offset != 0 {
		// content-script's handleProductsSearch has no offset/page support
		// yet; rather than silently return page 1 for a caller asking for
		// page 2, fail closed until pagination is actually wired.
		return shopping.ProductPage{}, &shopping.ValidationError{Operation: "products.search", Field: "offset", Problem: shopping.ValidationInvalid}
	}
	if search.Limit < 0 || search.Limit > maxProductsPerPage {
		return shopping.ProductPage{}, &shopping.ValidationError{Operation: "products.search", Field: "limit", Problem: shopping.ValidationInvalid}
	}

	params, err := json.Marshal(productsSearchParams{
		Term:           term,
		MarketID:       string(current.StoreID),
		ObjectsPerPage: search.Limit,
	})
	if err != nil {
		return shopping.ProductPage{}, err
	}

	result, err := g.Transport.Do(ctx, browserbridge.OperationProductsSearch, params)
	if err != nil {
		return shopping.ProductPage{}, classifyReadBridgeError("products.search", err)
	}

	payload, err := decodeCritical[productsSearchPayload]("products.search", result)
	if err != nil {
		return shopping.ProductPage{}, err
	}

	observedAt := g.now()
	products := make([]shopping.Product, 0, len(payload.Hits))
	for _, hit := range payload.Hits {
		products = append(products, shopping.Product{
			ID:         shopping.ProductID(hit.ListingID),
			Name:       hit.Title,
			Unit:       hit.Pricing.Grammage,
			Price:      shopping.Money{Cents: *hit.Pricing.CurrentRetailPrice, Currency: currencyEUR},
			Available:  true,
			ObservedAt: observedAt,
		})
	}

	hasMore := false
	if payload.Pagination != nil && payload.Pagination.PageCount > 0 {
		hasMore = payload.Pagination.CurrentPage+1 < payload.Pagination.PageCount
	}
	return shopping.ProductPage{Products: products, HasMore: hasMore, ObservedAt: observedAt}, nil
}

// classifyReadBridgeError is defined in bridge_errors.go, shared with the
// other two verticals.

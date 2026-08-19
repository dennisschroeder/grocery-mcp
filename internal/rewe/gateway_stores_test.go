package rewe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/rewe/contractfixture"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// capturingTransport records the last operation/params sent, unlike
// stubTransport (browser_validator_test.go) which only stubs the response.
type capturingTransport struct {
	result json.RawMessage
	err    error

	gotOp     browserbridge.Operation
	gotParams json.RawMessage
}

func (t *capturingTransport) Do(_ context.Context, op browserbridge.Operation, params json.RawMessage) (json.RawMessage, error) {
	t.gotOp = op
	t.gotParams = params
	return t.result, t.err
}

func TestStoresSearchFixtureMatchesSanitizerOutput(t *testing.T) {
	// A synthetic, never-real upstream-shaped input — the schema is the
	// authoritative shape for the checked-in fixture, not the other way
	// around.
	raw := []byte(`{
		"stores": [{
			"market_id": "upstream-market-778899", "name": "Upstream REWE",
			"postal_code": "upstream-99999", "city": "Upstreamtown", "distance_meters": 4242,
			"internal_debug": "discard me"
		}]
	}`)
	schema := contractfixture.Object(map[string]contractfixture.Schema{
		"stores": contractfixture.Array(contractfixture.Object(map[string]contractfixture.Schema{
			"market_id":       contractfixture.ReplaceWith("123456"),
			"name":            contractfixture.ReplaceWith("Example REWE"),
			"postal_code":     contractfixture.ReplaceWith("10115"),
			"city":            contractfixture.ReplaceWith("Berlin"),
			"distance_meters": contractfixture.ReplaceWith(1200),
		})),
	})
	got, err := contractfixture.SanitizeJSON(raw, schema)
	if err != nil {
		t.Fatalf("SanitizeJSON() error = %v", err)
	}
	want, err := os.ReadFile("testdata/stores-search.sanitized.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("SanitizeJSON() =\n%s\nwant:\n%s", got, want)
	}
	for _, forbidden := range []string{"internal_debug", "upstream-"} {
		if strings.Contains(string(got), forbidden) {
			t.Fatalf("fixture retains unreviewed upstream data (%q)", forbidden)
		}
	}
}

func TestProductsSearchLiveFixtureMatchesSanitizerOutput(t *testing.T) {
	raw := []byte(`{
		"hits": [{
			"listingId": "upstream-listing-9281", "productId": "upstream-product-1234",
			"title": "Upstream Milk",
			"pricing": {"currentRetailPrice": 129, "grammage": "1l", "internal": 12},
			"unreviewed": "discard me"
		}],
		"pagination": {"currentPage": 3, "objectCount": 400, "objectsPerPage": 10, "pageCount": 40}
	}`)
	schema := contractfixture.Object(map[string]contractfixture.Schema{
		"hits": contractfixture.Array(contractfixture.Object(map[string]contractfixture.Schema{
			"listingId": contractfixture.ReplaceWith("listing-1"),
			"title":     contractfixture.ReplaceWith("Example Product"),
			"pricing": contractfixture.Object(map[string]contractfixture.Schema{
				"currentRetailPrice": contractfixture.ReplaceWith(199),
				"grammage":           contractfixture.ReplaceWith("500g"),
			}),
		})),
		"pagination": contractfixture.Object(map[string]contractfixture.Schema{
			"currentPage": contractfixture.ReplaceWith(0),
			"pageCount":   contractfixture.ReplaceWith(1),
		}),
	})
	got, err := contractfixture.SanitizeJSON(raw, schema)
	if err != nil {
		t.Fatalf("SanitizeJSON() error = %v", err)
	}
	want, err := os.ReadFile("testdata/products-search-live.sanitized.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("SanitizeJSON() =\n%s\nwant:\n%s", got, want)
	}
	for _, forbidden := range []string{"unreviewed", "internal", "upstream-"} {
		if strings.Contains(string(got), forbidden) {
			t.Fatalf("fixture retains unreviewed upstream data (%q)", forbidden)
		}
	}
}

func TestGatewaySearchStoresDecodesFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/stores-search.sanitized.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	transport := &capturingTransport{result: fixture}
	observedAt := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	gateway := Gateway{Transport: transport, Now: func() time.Time { return observedAt }}

	page, err := gateway.SearchStores(t.Context(), shopping.ShoppingContext{}, shopping.StoreSearch{PostalCode: "10115"})
	if err != nil {
		t.Fatalf("SearchStores() error = %v", err)
	}
	if transport.gotOp != browserbridge.OperationStoresSearch {
		t.Fatalf("operation = %q, want %q", transport.gotOp, browserbridge.OperationStoresSearch)
	}
	var sentParams storesSearchParams
	if err := json.Unmarshal(transport.gotParams, &sentParams); err != nil {
		t.Fatalf("unmarshal sent params: %v", err)
	}
	if sentParams.PostalCode != "10115" {
		t.Fatalf("sent postal_code = %q, want %q", sentParams.PostalCode, "10115")
	}
	store := page.Stores[0]
	if len(page.Stores) != 1 || store.ID != "123456" || store.Name != "Example REWE" ||
		store.Address != (shopping.StoreAddress{PostalCode: "10115", City: "Berlin"}) ||
		!store.Pickup || store.DistanceMeters != 1200 {
		t.Fatalf("unexpected stores: %#v", page.Stores)
	}
	if page.ObservedAt != observedAt || page.Stores[0].ObservedAt != observedAt {
		t.Fatalf("unexpected ObservedAt: page=%v store=%v", page.ObservedAt, page.Stores[0].ObservedAt)
	}
	if page.HasMore {
		t.Fatal("HasMore should be false when no limit was applied")
	}
}

func TestGatewaySearchStoresAppliesLimit(t *testing.T) {
	fixture := json.RawMessage(`{"stores":[{"market_id":"111111","postal_code":"10115"},{"market_id":"222222","postal_code":"10115"}]}`)
	gateway := Gateway{Transport: stubTransport{result: fixture}}
	page, err := gateway.SearchStores(t.Context(), shopping.ShoppingContext{}, shopping.StoreSearch{PostalCode: "10115", Limit: 1})
	if err != nil {
		t.Fatalf("SearchStores() error = %v", err)
	}
	if len(page.Stores) != 1 || !page.HasMore {
		t.Fatalf("unexpected page: %#v", page)
	}
}

func TestGatewaySearchStoresRejectsMissingPostalCode(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{}}
	_, err := gateway.SearchStores(t.Context(), shopping.ShoppingContext{}, shopping.StoreSearch{})
	var target *shopping.ValidationError
	if !errors.As(err, &target) || target.Problem != shopping.ValidationMissing {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySearchStoresRejectsImplausiblePostalCode(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{}}
	_, err := gateway.SearchStores(t.Context(), shopping.ShoppingContext{}, shopping.StoreSearch{PostalCode: "not-a-zip"})
	var target *shopping.ValidationError
	if !errors.As(err, &target) || target.Problem != shopping.ValidationInvalid {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySearchStoresClassifiesMalformedPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed", `not-json`},
		{"missing stores", `{}`},
		{"null stores", `{"stores": null}`},
		{"wrong type", `{"stores": "nope"}`},
		{"empty market id", `{"stores": [{"market_id": "", "postal_code": "10115"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gateway := Gateway{Transport: stubTransport{result: json.RawMessage(test.body)}}
			_, err := gateway.SearchStores(t.Context(), shopping.ShoppingContext{}, shopping.StoreSearch{PostalCode: "10115"})
			var target *shopping.UpstreamChangeError
			if !errors.As(err, &target) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), test.body) {
				t.Fatal("error exposed the raw upstream payload")
			}
		})
	}
}

func TestGatewaySearchStoresMapsAuthInvalid(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{err: codedStubError{code: "auth_invalid"}}}
	_, err := gateway.SearchStores(t.Context(), shopping.ShoppingContext{}, shopping.StoreSearch{PostalCode: "10115"})
	var target *shopping.AuthError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySearchStoresMapsRateLimited(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{err: codedStubError{code: "rate_limited"}}}
	_, err := gateway.SearchStores(t.Context(), shopping.ShoppingContext{}, shopping.StoreSearch{PostalCode: "10115"})
	var target *shopping.RateLimitError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySearchStoresMapsBridgeUnavailable(t *testing.T) {
	for _, code := range []string{"content_script_unreachable", "canceled"} {
		t.Run(code, func(t *testing.T) {
			gateway := Gateway{Transport: stubTransport{err: codedStubError{code: code}}}
			_, err := gateway.SearchStores(t.Context(), shopping.ShoppingContext{}, shopping.StoreSearch{PostalCode: "10115"})
			var target *shopping.BridgeUnavailableError
			if !errors.As(err, &target) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGatewaySelectStoreBindsPlausibleID(t *testing.T) {
	gateway := Gateway{}
	next, err := gateway.SelectStore(t.Context(), shopping.ShoppingContext{AccountID: "account-1"}, shopping.StoreID("123456"))
	if err != nil {
		t.Fatalf("SelectStore() error = %v", err)
	}
	if next.StoreID != "123456" || next.AccountID != "account-1" {
		t.Fatalf("unexpected context: %#v", next)
	}
}

func TestGatewaySelectStoreMakesNoTransportCall(t *testing.T) {
	// nil Transport would panic if SelectStore ever tried to call it — per
	// the frozen contract, selecting a store is a pure local rebind.
	gateway := Gateway{Transport: nil}
	if _, err := gateway.SelectStore(t.Context(), shopping.ShoppingContext{}, shopping.StoreID("123456")); err != nil {
		t.Fatalf("SelectStore() error = %v", err)
	}
}

func TestGatewaySelectStoreRejectsEmptyID(t *testing.T) {
	gateway := Gateway{}
	_, err := gateway.SelectStore(t.Context(), shopping.ShoppingContext{}, shopping.StoreID(""))
	var target *shopping.ValidationError
	if !errors.As(err, &target) || target.Problem != shopping.ValidationMissing {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySelectStoreRejectsImplausibleID(t *testing.T) {
	gateway := Gateway{}
	_, err := gateway.SelectStore(t.Context(), shopping.ShoppingContext{}, shopping.StoreID("<script>alert(1)</script>"))
	var target *shopping.ValidationError
	if !errors.As(err, &target) || target.Problem != shopping.ValidationInvalid {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySearchProductsDecodesFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/products-search-live.sanitized.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	transport := &capturingTransport{result: fixture}
	observedAt := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	gateway := Gateway{Transport: transport, Now: func() time.Time { return observedAt }}

	sc := shopping.ShoppingContext{StoreID: "123456"}
	page, err := gateway.SearchProducts(t.Context(), sc, shopping.ProductSearch{Query: "milch", Limit: 10})
	if err != nil {
		t.Fatalf("SearchProducts() error = %v", err)
	}
	if transport.gotOp != browserbridge.OperationProductsSearch {
		t.Fatalf("operation = %q, want %q", transport.gotOp, browserbridge.OperationProductsSearch)
	}
	var sentParams productsSearchParams
	if err := json.Unmarshal(transport.gotParams, &sentParams); err != nil {
		t.Fatalf("unmarshal sent params: %v", err)
	}
	if sentParams.Term != "milch" || sentParams.MarketID != "123456" || sentParams.ObjectsPerPage != 10 {
		t.Fatalf("unexpected sent params: %#v", sentParams)
	}
	if len(page.Products) != 1 {
		t.Fatalf("unexpected products: %#v", page.Products)
	}
	product := page.Products[0]
	if product.ID != "listing-1" || product.Name != "Example Product" ||
		product.Unit != "500g" || !product.Available || product.Price != (shopping.Money{Cents: 199, Currency: "EUR"}) {
		t.Fatalf("unexpected product: %#v", product)
	}
	if product.ObservedAt != observedAt || page.ObservedAt != observedAt {
		t.Fatalf("unexpected ObservedAt: product=%v page=%v", product.ObservedAt, page.ObservedAt)
	}
	if page.HasMore {
		t.Fatal("HasMore should be false when number+1 == totalPages")
	}
}

func TestGatewaySearchProductsComputesHasMoreFromPage(t *testing.T) {
	fixture := json.RawMessage(`{
		"hits": [{"listingId":"l1","title":"n","pricing":{"currentRetailPrice":100}}],
		"pagination": {"currentPage": 0, "pageCount": 3}
	}`)
	gateway := Gateway{Transport: stubTransport{result: fixture}}
	page, err := gateway.SearchProducts(t.Context(), shopping.ShoppingContext{StoreID: "123456"}, shopping.ProductSearch{Query: "milch"})
	if err != nil {
		t.Fatalf("SearchProducts() error = %v", err)
	}
	if !page.HasMore {
		t.Fatal("HasMore should be true when more pages remain")
	}
}

func TestGatewaySearchProductsTreatsMissingPageAsNoMore(t *testing.T) {
	fixture := json.RawMessage(`{"hits": []}`)
	gateway := Gateway{Transport: stubTransport{result: fixture}}
	page, err := gateway.SearchProducts(t.Context(), shopping.ShoppingContext{StoreID: "123456"}, shopping.ProductSearch{Query: "milch"})
	if err != nil {
		t.Fatalf("SearchProducts() error = %v", err)
	}
	if page.HasMore || len(page.Products) != 0 {
		t.Fatalf("unexpected page: %#v", page)
	}
}

func TestGatewaySearchProductsRejectsEmptyQuery(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{}}
	_, err := gateway.SearchProducts(t.Context(), shopping.ShoppingContext{StoreID: "123456"}, shopping.ProductSearch{})
	var target *shopping.ValidationError
	if !errors.As(err, &target) || target.Field != "query" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySearchProductsRejectsMissingStore(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{}}
	_, err := gateway.SearchProducts(t.Context(), shopping.ShoppingContext{}, shopping.ProductSearch{Query: "milch"})
	var target *shopping.ValidationError
	if !errors.As(err, &target) || target.Field != "store_id" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySearchProductsRejectsNonzeroOffset(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{}}
	_, err := gateway.SearchProducts(t.Context(), shopping.ShoppingContext{StoreID: "123456"}, shopping.ProductSearch{Query: "milch", Offset: 1})
	var target *shopping.ValidationError
	if !errors.As(err, &target) || target.Field != "offset" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySearchProductsRejectsOutOfRangeLimit(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{}}
	_, err := gateway.SearchProducts(t.Context(), shopping.ShoppingContext{StoreID: "123456"}, shopping.ProductSearch{Query: "milch", Limit: maxProductsPerPage + 1})
	var target *shopping.ValidationError
	if !errors.As(err, &target) || target.Field != "limit" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySearchProductsClassifiesMalformedPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed", `not-json`},
		{"missing hits", `{}`},
		{"null hits", `{"hits": null}`},
		{"hits wrong type", `{"hits": "nope"}`},
		{"missing listingId", `{"hits": [{"pricing": {"currentRetailPrice":100}}]}`},
		{"missing pricing", `{"hits": [{"listingId":"l1"}]}`},
		{"missing currentRetailPrice", `{"hits": [{"listingId":"l1","pricing":{}}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gateway := Gateway{Transport: stubTransport{result: json.RawMessage(test.body)}}
			_, err := gateway.SearchProducts(t.Context(), shopping.ShoppingContext{StoreID: "123456"}, shopping.ProductSearch{Query: "milch"})
			var target *shopping.UpstreamChangeError
			if !errors.As(err, &target) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGatewaySearchProductsMapsBridgeErrors(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{err: codedStubError{code: "auth_invalid"}}}
	_, err := gateway.SearchProducts(t.Context(), shopping.ShoppingContext{StoreID: "123456"}, shopping.ProductSearch{Query: "milch"})
	var target *shopping.AuthError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

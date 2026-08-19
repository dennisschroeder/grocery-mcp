package rewe

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/rewe/contractfixture"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

func moneySchemaForTest(cents int, currency string) contractfixture.Schema {
	return contractfixture.Object(map[string]contractfixture.Schema{
		"cents":    contractfixture.ReplaceWith(cents),
		"currency": contractfixture.ReplaceWith(currency),
	})
}

func orderFieldsSchemaForTest() map[string]contractfixture.Schema {
	return map[string]contractfixture.Schema{
		"id":       contractfixture.ReplaceWith("order-1"),
		"status":   contractfixture.ReplaceWith("READY"),
		"marketId": contractfixture.ReplaceWith("market-1"),
		"pickupAt": contractfixture.ReplaceWith("2026-08-20T10:00:00Z"),
		"total":    moneySchemaForTest(2599, "EUR"),
		"items": contractfixture.Array(contractfixture.Object(map[string]contractfixture.Schema{
			"productId": contractfixture.ReplaceWith("product-1"),
			"name":      contractfixture.ReplaceWith("Example Product"),
			"quantity":  contractfixture.ReplaceWith(2),
			"unitPrice": moneySchemaForTest(199, "EUR"),
			"lineTotal": moneySchemaForTest(398, "EUR"),
		})),
	}
}

// TestOrdersFixturesAreSanitizerDerived proves every internal/rewe/testdata
// orders fixture is contractfixture.SanitizeJSON's output for a documented
// schema, not a hand-typed stand-in — the same guarantee
// contractfixture/sanitize_test.go gives product-search.sanitized.json.
func TestOrdersFixturesAreSanitizerDerived(t *testing.T) {
	tests := []struct {
		file   string
		input  string
		schema contractfixture.Schema
	}{
		{
			file: "orders-list.sanitized.json",
			input: `{"orders":[{"id":"upstream-order-9281","status":"OPEN","marketId":"upstream-market-42",` +
				`"pickupAt":"2026-08-20T10:00:00Z","total":{"cents":2599,"currency":"EUR","internal":1},"unreviewed":"discard me"}]}`,
			schema: contractfixture.Object(map[string]contractfixture.Schema{
				"orders": contractfixture.Array(contractfixture.Object(map[string]contractfixture.Schema{
					"id":       contractfixture.ReplaceWith("order-1"),
					"status":   contractfixture.ReplaceWith("OPEN"),
					"marketId": contractfixture.ReplaceWith("market-1"),
					"pickupAt": contractfixture.ReplaceWith("2026-08-20T10:00:00Z"),
					"total":    moneySchemaForTest(2599, "EUR"),
				})),
			}),
		},
		{
			file: "order-get.sanitized.json",
			input: `{"id":"upstream-order-9281","status":"READY","marketId":"upstream-market-42",` +
				`"pickupAt":"2026-08-20T10:00:00Z","total":{"cents":2599,"currency":"EUR"},` +
				`"items":[{"productId":"upstream-product-1","name":"Bio Vollmilch 3.5%","quantity":2,` +
				`"unitPrice":{"cents":199,"currency":"EUR"},"lineTotal":{"cents":398,"currency":"EUR"}}],"unreviewed":"discard me"}`,
			schema: contractfixture.Object(orderFieldsSchemaForTest()),
		},
		{
			file: "order-get-wrapped.sanitized.json",
			input: `{"orderDetails":{"id":"upstream-order-9281","status":"READY","marketId":"upstream-market-42",` +
				`"pickupAt":"2026-08-20T10:00:00Z","total":{"cents":2599,"currency":"EUR"},` +
				`"items":[{"productId":"upstream-product-1","name":"Bio Vollmilch 3.5%","quantity":2,` +
				`"unitPrice":{"cents":199,"currency":"EUR"},"lineTotal":{"cents":398,"currency":"EUR"}}]}}`,
			schema: contractfixture.Object(map[string]contractfixture.Schema{
				"orderDetails": contractfixture.Object(orderFieldsSchemaForTest()),
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			got, err := contractfixture.SanitizeJSON([]byte(test.input), test.schema)
			if err != nil {
				t.Fatalf("SanitizeJSON() error = %v", err)
			}
			want, err := os.ReadFile("testdata/" + test.file)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("SanitizeJSON() =\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func readFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func TestGatewayListOrdersDecodesFixture(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	gateway := Gateway{
		Transport: stubTransport{result: readFixture(t, "orders-list.sanitized.json")},
		Now:       func() time.Time { return observedAt },
	}
	page, err := gateway.ListOrders(t.Context(), shopping.ShoppingContext{}, shopping.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if page.HasMore {
		t.Fatal("HasMore = true, want false")
	}
	if page.ObservedAt != observedAt {
		t.Fatalf("ObservedAt = %v, want %v", page.ObservedAt, observedAt)
	}
	want := shopping.OrderSummary{
		ID:         "order-1",
		Status:     shopping.OrderStatusOpen,
		StoreID:    "market-1",
		PickupAt:   time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
		Total:      shopping.Money{Cents: 2599, Currency: "EUR"},
		ObservedAt: observedAt,
	}
	if len(page.Orders) != 1 || page.Orders[0] != want {
		t.Fatalf("Orders = %#v, want [%#v]", page.Orders, want)
	}
}

func TestGatewayListOrdersPaginatesClientSide(t *testing.T) {
	body := json.RawMessage(`{"orders":[
		{"id":"order-1","total":{"cents":100,"currency":"EUR"}},
		{"id":"order-2","total":{"cents":200,"currency":"EUR"}},
		{"id":"order-3","total":{"cents":300,"currency":"EUR"}}
	]}`)
	gateway := Gateway{Transport: stubTransport{result: body}, Now: func() time.Time { return time.Unix(0, 0) }}

	page, err := gateway.ListOrders(t.Context(), shopping.ShoppingContext{}, shopping.PageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Orders) != 2 || page.Orders[0].ID != "order-1" || page.Orders[1].ID != "order-2" {
		t.Fatalf("Orders = %#v", page.Orders)
	}
	if !page.HasMore {
		t.Fatal("HasMore = false, want true")
	}

	page, err = gateway.ListOrders(t.Context(), shopping.ShoppingContext{}, shopping.PageRequest{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Orders) != 1 || page.Orders[0].ID != "order-3" {
		t.Fatalf("Orders = %#v", page.Orders)
	}
	if page.HasMore {
		t.Fatal("HasMore = true, want false")
	}

	page, err = gateway.ListOrders(t.Context(), shopping.ShoppingContext{}, shopping.PageRequest{Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Orders) != 0 || page.HasMore {
		t.Fatalf("out-of-range offset: Orders = %#v, HasMore = %v", page.Orders, page.HasMore)
	}
}

func TestGatewayListOrdersRejectsBareArrayShape(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{result: json.RawMessage(`[{"id":"order-1"}]`)}}
	_, err := gateway.ListOrders(t.Context(), shopping.ShoppingContext{}, shopping.PageRequest{})
	var upstreamErr *shopping.UpstreamChangeError
	if !errors.As(err, &upstreamErr) || upstreamErr.Problem != shopping.UpstreamTypeChanged {
		t.Fatalf("err = %v, want UpstreamChangeError{Problem: UpstreamTypeChanged}", err)
	}
}

func TestGatewayListOrdersRejectsMissingOrderID(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{result: json.RawMessage(`{"orders":[{"status":"OPEN"}]}`)}}
	_, err := gateway.ListOrders(t.Context(), shopping.ShoppingContext{}, shopping.PageRequest{})
	var upstreamErr *shopping.UpstreamChangeError
	if !errors.As(err, &upstreamErr) || upstreamErr.Problem != shopping.UpstreamMissingCriticalField {
		t.Fatalf("err = %v, want UpstreamChangeError{Problem: UpstreamMissingCriticalField}", err)
	}
}

func TestGatewayGetOrderRejectsEmptyID(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{}}
	_, err := gateway.GetOrder(t.Context(), shopping.ShoppingContext{}, "")
	var validationErr *shopping.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Problem != shopping.ValidationMissing {
		t.Fatalf("err = %v, want ValidationError{Problem: ValidationMissing}", err)
	}
}

func TestGatewayGetOrderDecodesRootShape(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	gateway := Gateway{
		Transport: stubTransport{result: readFixture(t, "order-get.sanitized.json")},
		Now:       func() time.Time { return observedAt },
	}
	order, err := gateway.GetOrder(t.Context(), shopping.ShoppingContext{}, "order-1")
	if err != nil {
		t.Fatal(err)
	}
	assertDecodedOrder(t, order, observedAt)
}

func TestGatewayGetOrderDecodesOrderDetailsWrapperShape(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	gateway := Gateway{
		Transport: stubTransport{result: readFixture(t, "order-get-wrapped.sanitized.json")},
		Now:       func() time.Time { return observedAt },
	}
	order, err := gateway.GetOrder(t.Context(), shopping.ShoppingContext{}, "order-1")
	if err != nil {
		t.Fatal(err)
	}
	assertDecodedOrder(t, order, observedAt)
}

func assertDecodedOrder(t *testing.T, order shopping.Order, observedAt time.Time) {
	t.Helper()
	wantSummary := shopping.OrderSummary{
		ID:         "order-1",
		Status:     shopping.OrderStatusReady,
		StoreID:    "market-1",
		PickupAt:   time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
		Total:      shopping.Money{Cents: 2599, Currency: "EUR"},
		ObservedAt: observedAt,
	}
	if order.OrderSummary != wantSummary {
		t.Fatalf("OrderSummary = %#v, want %#v", order.OrderSummary, wantSummary)
	}
	wantItem := shopping.BasketItem{
		ProductID: "product-1",
		Name:      "Example Product",
		Quantity:  2,
		UnitPrice: shopping.Money{Cents: 199, Currency: "EUR"},
		LineTotal: shopping.Money{Cents: 398, Currency: "EUR"},
	}
	if len(order.Items) != 1 || order.Items[0] != wantItem {
		t.Fatalf("Items = %#v, want [%#v]", order.Items, wantItem)
	}
}

// TestGatewayGetOrderFailsClosedWhenNeitherShapeHasID covers the
// orderDetails-wrapper-vs-root ambiguity the contract flags: a response
// that has neither a root "id" nor an "orderDetails.id" must not silently
// decode as a zero-value order.
func TestGatewayGetOrderFailsClosedWhenNeitherShapeHasID(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{result: json.RawMessage(`{"status":"READY"}`)}}
	_, err := gateway.GetOrder(t.Context(), shopping.ShoppingContext{}, "order-1")
	var upstreamErr *shopping.UpstreamChangeError
	if !errors.As(err, &upstreamErr) || upstreamErr.Problem != shopping.UpstreamMissingCriticalField {
		t.Fatalf("err = %v, want UpstreamChangeError{Problem: UpstreamMissingCriticalField}", err)
	}
}

func TestGatewayGetOrderIgnoresNonObjectOrderDetails(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{result: json.RawMessage(`{"id":"order-1","orderDetails":null}`)}}
	order, err := gateway.GetOrder(t.Context(), shopping.ShoppingContext{}, "order-1")
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != "order-1" {
		t.Fatalf("ID = %q, want order-1", order.ID)
	}
}

func TestMapTransportErrorClassifiesCodedErrors(t *testing.T) {
	tests := []struct {
		code string
		as   func(error) bool
	}{
		{"auth_invalid", func(err error) bool { var target *shopping.AuthError; return errors.As(err, &target) }},
		{"rate_limited", func(err error) bool { var target *shopping.RateLimitError; return errors.As(err, &target) }},
		{"content_script_unreachable", func(err error) bool { var target *shopping.BridgeUnavailableError; return errors.As(err, &target) }},
		{"canceled", func(err error) bool { var target *shopping.BridgeUnavailableError; return errors.As(err, &target) }},
		{"invalid_params", func(err error) bool { var target *shopping.ValidationError; return errors.As(err, &target) }},
		{"not_implemented", func(err error) bool { var target *shopping.UpstreamChangeError; return errors.As(err, &target) }},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			gateway := Gateway{Transport: stubTransport{err: codedStubError{code: test.code}}}
			_, err := gateway.ListOrders(t.Context(), shopping.ShoppingContext{}, shopping.PageRequest{})
			if !test.as(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMapTransportErrorClassifiesUncodedErrors(t *testing.T) {
	gateway := Gateway{Transport: stubTransport{err: errors.New("dial bridge socket: connection refused")}}
	_, err := gateway.ListOrders(t.Context(), shopping.ShoppingContext{}, shopping.PageRequest{})
	var upstreamErr *shopping.UpstreamChangeError
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

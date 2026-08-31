package rewe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/rewe/contractfixture"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

var fixedNow = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

func fixedGateway(transport browserbridge.Transport) Gateway {
	return Gateway{Transport: transport, Now: func() time.Time { return fixedNow }}
}

type countingTransport struct {
	calls int
}

type sequenceTransport struct {
	results []json.RawMessage
	errors  []error
	calls   int
}

func (t *sequenceTransport) Do(context.Context, browserbridge.Operation, json.RawMessage) (json.RawMessage, error) {
	index := t.calls
	t.calls++
	if index < len(t.errors) && t.errors[index] != nil {
		return nil, t.errors[index]
	}
	return t.results[index], nil
}

func (t *countingTransport) Do(context.Context, browserbridge.Operation, json.RawMessage) (json.RawMessage, error) {
	t.calls++
	return nil, errors.New("should not be called")
}

func basketFixtureSchema() contractfixture.Schema {
	return contractfixture.Object(map[string]contractfixture.Schema{
		"basket": contractfixture.Object(map[string]contractfixture.Schema{
			"id": contractfixture.ReplaceWith("basket-1"),
			"serviceSelection": contractfixture.Object(map[string]contractfixture.Schema{
				"wwIdent": contractfixture.ReplaceWith("660500"),
			}),
			"lineItems": contractfixture.Array(contractfixture.Object(map[string]contractfixture.Schema{
				"quantity":   contractfixture.ReplaceWith(1),
				"price":      contractfixture.ReplaceWith(199),
				"totalPrice": contractfixture.ReplaceWith(199),
				"product": contractfixture.Object(map[string]contractfixture.Schema{
					"title": contractfixture.ReplaceWith("Example Product"),
					"listing": contractfixture.Object(map[string]contractfixture.Schema{
						"listingId": contractfixture.ReplaceWith("product-1"),
					}),
				}),
			})),
			"summary": contractfixture.Object(map[string]contractfixture.Schema{
				"articleCount": contractfixture.ReplaceWith(1),
				"articlePrice": contractfixture.ReplaceWith(199),
				"totalPrice":   contractfixture.ReplaceWith(249),
				"fees": contractfixture.Object(map[string]contractfixture.Schema{
					"substitutesSurcharge":  contractfixture.ReplaceWith(30),
					"transportBoxSurcharge": contractfixture.ReplaceWith(20),
				}),
			}),
		}),
	})
}

func timeSlotsFixtureSchema() contractfixture.Schema {
	return contractfixture.Array(contractfixture.Object(map[string]contractfixture.Schema{
		"id":         contractfixture.ReplaceWith("timeslot-1"),
		"startTime":  contractfixture.ReplaceWith("2026-08-20T08:00:00+02:00"),
		"endTime":    contractfixture.ReplaceWith("2026-08-20T09:00:00+02:00"),
		"serviceFee": contractfixture.ReplaceWith(0),
		"available":  contractfixture.ReplaceWith(true),
		"selected":   contractfixture.ReplaceWith(false),
	}))
}

// TestBasketFixtureMatchesSanitizedTestdata regenerates
// testdata/basket.sanitized.json from a representative (never-committed)
// raw REWE shape and asserts it matches the committed, reviewed fixture —
// the same pattern contractfixture/sanitize_test.go uses for
// product-search.sanitized.json.
func TestBasketFixtureMatchesSanitizedTestdata(t *testing.T) {
	input := []byte(`{
  "basket": {
    "id": "upstream-basket-9876543",
    "version": 7,
    "serviceSelection": {"wwIdent": "upstream-mkt-551234", "serviceType": "PICKUP", "zipCode": "50667"},
    "lineItems": [{
      "quantity": 3,
      "price": 129,
      "totalPrice": 387,
      "product": {"title": "Upstream Product Name", "listing": {"listingId": "upstream-product-42"}}
    }],
    "summary": {"articleCount": 3, "articlePrice": 387, "totalPrice": 437, "fees": {"substitutesSurcharge": 15, "transportBoxSurcharge": 10}},
    "unreviewedInternal": "discard me"
  }
}`)

	got, err := contractfixture.SanitizeJSON(input, basketFixtureSchema())
	if err != nil {
		t.Fatalf("SanitizeJSON() error = %v", err)
	}
	want, err := os.ReadFile("testdata/basket.sanitized.json")
	if err != nil {
		t.Fatalf("read sanitized fixture: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("SanitizeJSON() =\n%s\nwant:\n%s", got, want)
	}
}

func TestTimeSlotsFixtureMatchesSanitizedTestdata(t *testing.T) {
	input := []byte(`[{
    "id": "upstream-slot-778899",
    "startTime": "2026-08-20T08:00:00+02:00",
    "endTime": "2026-08-20T09:00:00+02:00",
    "serviceFee": 0,
    "available": true,
	"selected": false,
    "internalDebug": "discard me"
  }]`)

	got, err := contractfixture.SanitizeJSON(input, timeSlotsFixtureSchema())
	if err != nil {
		t.Fatalf("SanitizeJSON() error = %v", err)
	}
	want, err := os.ReadFile("testdata/timeslots.sanitized.json")
	if err != nil {
		t.Fatalf("read sanitized fixture: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("SanitizeJSON() =\n%s\nwant:\n%s", got, want)
	}
}

// --- basketPayload / timeSlot decode contract tests, matching decode_test.go's style ---

func TestDecodeBasketPayloadAcceptsSanitizedFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/basket.sanitized.json")
	if err != nil {
		t.Fatalf("read sanitized fixture: %v", err)
	}
	payload, err := decodeCritical[basketPayload]("basket.get", unwrapBasketDocument(fixture))
	if err != nil {
		t.Fatalf("decodeCritical() error = %v", err)
	}
	if payload.ID != "basket-1" || payload.ServiceSelection.WwIdent != "660500" {
		t.Fatalf("decodeCritical() = %#v", payload)
	}
	if len(payload.LineItems) != 1 || payload.LineItems[0].Product.Listing.ListingID != "product-1" {
		t.Fatalf("decodeCritical() lineItems = %#v", payload.LineItems)
	}
}

func TestDecodeBasketPayloadClassifiesIncompatiblePayloads(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		problem shopping.UpstreamProblem
	}{
		{"malformed JSON", `{"id":`, shopping.UpstreamMalformedJSON},
		{"missing id", `{"lineItems":[],"summary":{}}`, shopping.UpstreamMissingCriticalField},
		{"missing lineItems", `{"id":"basket-1","summary":{}}`, shopping.UpstreamMissingCriticalField},
		{"missing summary", `{"id":"basket-1","lineItems":[]}`, shopping.UpstreamMissingCriticalField},
		{"null id", `{"id":null,"lineItems":[],"summary":{}}`, shopping.UpstreamTypeChanged},
		{"wrong type id", `{"id":42,"lineItems":[],"summary":{}}`, shopping.UpstreamTypeChanged},
		{"trailing payload", `{"id":"basket-1","lineItems":[],"summary":{}}{}`, shopping.UpstreamTrailingPayload},
		{"line item missing product id", `{"id":"basket-1","lineItems":[{"quantity":1,"product":{}}],"summary":{}}`, shopping.UpstreamMissingCriticalField},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeCritical[basketPayload]("basket.get", []byte(test.body))
			var upstreamErr *shopping.UpstreamChangeError
			if !errors.As(err, &upstreamErr) {
				t.Fatalf("decodeCritical() error = %T, want *shopping.UpstreamChangeError", err)
			}
			if upstreamErr.Problem != test.problem {
				t.Fatalf("problem = %q, want %q", upstreamErr.Problem, test.problem)
			}
		})
	}
}

func TestUnwrapBasketDocumentAcceptsBareOrWrappedShape(t *testing.T) {
	wrapped := []byte(`{"basket":{"id":"basket-1","lineItems":[],"summary":{}}}`)
	bare := []byte(`{"id":"basket-1","lineItems":[],"summary":{}}`)

	wrappedPayload, err := decodeCritical[basketPayload]("basket.get", unwrapBasketDocument(wrapped))
	if err != nil {
		t.Fatalf("wrapped decodeCritical() error = %v", err)
	}
	barePayload, err := decodeCritical[basketPayload]("basket.get", unwrapBasketDocument(bare))
	if err != nil {
		t.Fatalf("bare decodeCritical() error = %v", err)
	}
	if wrappedPayload.ID != "basket-1" || barePayload.ID != "basket-1" {
		t.Fatalf("unexpected ids: wrapped=%q bare=%q", wrappedPayload.ID, barePayload.ID)
	}
}

func TestDecodeTimeSlotsAcceptsSanitizedFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/timeslots.sanitized.json")
	if err != nil {
		t.Fatalf("read sanitized fixture: %v", err)
	}
	slots, err := decodeTimeSlots("timeslots.list", "660500", fixedNow, fixture)
	if err != nil {
		t.Fatalf("decodeTimeSlots() error = %v", err)
	}
	if len(slots) != 1 || slots[0].ID != "timeslot-1" || slots[0].StoreID != "660500" || !slots[0].Available {
		t.Fatalf("decodeTimeSlots() = %#v", slots)
	}
}

func TestDecodeTimeSlotsClassifiesIncompatiblePayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"malformed JSON", `[{"id":`},
		{"object without timeSlots key", `{"foo":"bar"}`},
		{"null timeSlots", `{"timeSlots":null}`},
		{"array item wrong type", `[{"id":"slot-1","startTime":123,"endTime":"2026-08-20T09:00:00Z"}]`},
		{"array item missing id", `[{"startTime":"2026-08-20T08:00:00Z","endTime":"2026-08-20T09:00:00Z"}]`},
		{"array item bad start time", `[{"id":"slot-1","startTime":"not-a-time","endTime":"2026-08-20T09:00:00Z"}]`},
		{"trailing payload", `[]{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeTimeSlots("timeslots.list", "660500", fixedNow, []byte(test.body))
			var upstreamErr *shopping.UpstreamChangeError
			if !errors.As(err, &upstreamErr) {
				t.Fatalf("decodeTimeSlots() error = %T, want *shopping.UpstreamChangeError", err)
			}
		})
	}
}

func TestDecodeTimeSlotsAcceptsTimeSlotsWrapper(t *testing.T) {
	body := []byte(`{"timeSlots":[{"id":"slot-1","startTime":"2026-08-20T08:00:00Z","endTime":"2026-08-20T09:00:00Z","serviceFee":0}]}`)
	slots, err := decodeTimeSlots("timeslots.list", "660500", fixedNow, body)
	if err != nil {
		t.Fatalf("decodeTimeSlots() error = %v", err)
	}
	// available defaults true when REWE's response omits the field.
	if len(slots) != 1 || !slots[0].Available {
		t.Fatalf("decodeTimeSlots() = %#v", slots)
	}
}

// --- GetBasket ---

func TestGatewayGetBasketListingsReturnsValidatedIDs(t *testing.T) {
	transport := &capturingTransport{result: json.RawMessage(`{"listing_ids":["listing-new","listing-old"]}`)}
	got, err := fixedGateway(transport).GetBasketListings(t.Context(), shopping.ShoppingContext{})
	if err != nil {
		t.Fatalf("GetBasketListings() error = %v", err)
	}
	if transport.gotOp != browserbridge.OperationBasketListingsGet || len(transport.gotParams) != 0 {
		t.Fatalf("transport call = %q %s", transport.gotOp, transport.gotParams)
	}
	if len(got.ListingIDs) != 2 || got.ListingIDs[0] != "listing-new" || got.ListingIDs[1] != "listing-old" {
		t.Fatalf("GetBasketListings() = %#v", got)
	}
	if !got.ObservedAt.Equal(fixedNow) {
		t.Fatalf("ObservedAt = %v, want %v", got.ObservedAt, fixedNow)
	}
}

func TestGatewayGetBasketListingsRejectsInvalidOrDuplicateIDs(t *testing.T) {
	for name, result := range map[string]json.RawMessage{
		"missing field": json.RawMessage(`{}`),
		"invalid id":    json.RawMessage(`{"listing_ids":["listing/unsafe"]}`),
		"duplicate":     json.RawMessage(`{"listing_ids":["listing-1","listing-1"]}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := fixedGateway(stubTransport{result: result}).GetBasketListings(t.Context(), shopping.ShoppingContext{})
			var upstreamErr *shopping.UpstreamChangeError
			if !errors.As(err, &upstreamErr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGatewayGetBasketRequiresBoundBasketID(t *testing.T) {
	transport := stubTransport{result: json.RawMessage(`{"found":false}`)}
	_, err := fixedGateway(transport).GetBasket(t.Context(), shopping.ShoppingContext{})
	var validationErr *shopping.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "basket_id" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewayGetBasketDecodesFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/basket.sanitized.json")
	if err != nil {
		t.Fatalf("read sanitized fixture: %v", err)
	}
	gateway := fixedGateway(stubTransport{result: json.RawMessage(fixture)})
	basket, err := gateway.GetBasket(t.Context(), shopping.ShoppingContext{BasketID: "basket-1"})
	if err != nil {
		t.Fatalf("GetBasket() error = %v", err)
	}
	if basket.ID != "basket-1" || basket.StoreID != "660500" {
		t.Fatalf("GetBasket() = %#v", basket)
	}
	if len(basket.Items) != 1 || basket.Items[0].ProductID != "product-1" || basket.Items[0].Quantity != 1 {
		t.Fatalf("GetBasket() items = %#v", basket.Items)
	}
	if basket.Subtotal.Cents != 199 || basket.Total.Cents != 249 || basket.Fees.Cents != 50 {
		t.Fatalf("GetBasket() money = subtotal:%#v total:%#v fees:%#v", basket.Subtotal, basket.Total, basket.Fees)
	}
	if !basket.ObservedAt.Equal(fixedNow) {
		t.Fatalf("ObservedAt = %v, want %v", basket.ObservedAt, fixedNow)
	}
}

func TestGatewayGetBasketMapsCodedErrors(t *testing.T) {
	tests := []struct {
		name string
		code string
		as   func(error) bool
	}{
		{"auth invalid", "auth_invalid", func(err error) bool { var target *shopping.AuthError; return errors.As(err, &target) }},
		{"rate limited", "rate_limited", func(err error) bool { var target *shopping.RateLimitError; return errors.As(err, &target) }},
		{"content script unreachable", "content_script_unreachable", func(err error) bool { var target *shopping.BridgeUnavailableError; return errors.As(err, &target) }},
		{"canceled", "canceled", func(err error) bool { var target *shopping.BridgeUnavailableError; return errors.As(err, &target) }},
		{"unrecognized code", "something_new", func(err error) bool { var target *shopping.UpstreamChangeError; return errors.As(err, &target) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gateway := fixedGateway(stubTransport{err: codedStubError{code: test.code}})
			_, err := gateway.GetBasket(t.Context(), shopping.ShoppingContext{BasketID: "basket-1"})
			if !test.as(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// --- ApplyBasket ---

func TestGatewayApplyBasketRequiresChanges(t *testing.T) {
	transport := &countingTransport{}
	_, err := fixedGateway(transport).ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{})
	var validationErr *shopping.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "changes" {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport.calls != 0 {
		t.Fatalf("transport called %d times, want 0", transport.calls)
	}
}

func TestGatewayApplyBasketAppliesExactMatch(t *testing.T) {
	result := json.RawMessage(`{"changes":[{"listing_id":"p1","ok":true,"result":{"basket":{"id":"basket-9","serviceSelection":{"wwIdent":"660500"},"lineItems":[{"quantity":2,"price":100,"totalPrice":200,"product":{"title":"Milk","listing":{"listingId":"p1"}}}],"summary":{"articleCount":1,"articlePrice":200,"totalPrice":200}}}}]}`)
	gateway := fixedGateway(stubTransport{result: result})
	got, err := gateway.ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{
		Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	if len(got.Outcomes) != 1 {
		t.Fatalf("ApplyBasket() outcomes = %#v", got.Outcomes)
	}
	outcome := got.Outcomes[0]
	if outcome.Status != shopping.BasketChangeApplied || outcome.AppliedQuantity != 2 || outcome.RequestedQuantity != 2 {
		t.Fatalf("outcome = %#v", outcome)
	}
	if got.Basket.ID != "basket-9" {
		t.Fatalf("Basket = %#v", got.Basket)
	}
}

func TestGatewayApplyBasketUsesAuthoritativeReconciliationForOutcomes(t *testing.T) {
	result := json.RawMessage(`{
		"changes":[{"listing_id":"p1","ok":true,"result":{"basket":{"id":"basket-9","lineItems":[{"quantity":2,"price":100,"totalPrice":200,"product":{"title":"Milk","listing":{"listingId":"p1"}}}],"summary":{"articleCount":1,"articlePrice":200,"totalPrice":200}}}}],
		"basket":{"basket":{"id":"basket-9","serviceSelection":{"wwIdent":"660500"},"lineItems":[{"quantity":1,"price":100,"totalPrice":100,"product":{"title":"Milk","listing":{"listingId":"p1"}}}],"summary":{"articleCount":1,"articlePrice":100,"totalPrice":100}}},
		"reconciliation_code":""
	}`)
	gateway := fixedGateway(stubTransport{result: result})
	got, err := gateway.ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{
		Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	if !got.Reconciled || got.ReconciliationProblem != "" {
		t.Fatalf("reconciliation = %v, %q", got.Reconciled, got.ReconciliationProblem)
	}
	if got.Outcomes[0].Status != shopping.BasketChangeAdjusted || got.Outcomes[0].AppliedQuantity != 1 {
		t.Fatalf("outcome = %#v", got.Outcomes[0])
	}
}

func TestGatewayApplyBasketDetectsAdjustedQuantity(t *testing.T) {
	// REWE returns a snapshot showing quantity 3 even though 5 was
	// requested (e.g. a stock limit) — must surface as Adjusted, not
	// silently reported as if the full request succeeded.
	result := json.RawMessage(`{"changes":[{"listing_id":"p1","ok":true,"result":{"basket":{"id":"basket-9","lineItems":[{"quantity":3,"price":100,"totalPrice":300,"product":{"title":"Milk","listing":{"listingId":"p1"}}}],"summary":{"articleCount":1,"articlePrice":300,"totalPrice":300}}}}]}`)
	gateway := fixedGateway(stubTransport{result: result})
	got, err := gateway.ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{
		Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 5}},
	})
	if err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	outcome := got.Outcomes[0]
	if outcome.Status != shopping.BasketChangeAdjusted || outcome.AppliedQuantity != 3 || outcome.RequestedQuantity != 5 {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestGatewayApplyBasketReportsPerItemRejectionWithoutFailingWholeCall(t *testing.T) {
	transport := &sequenceTransport{results: []json.RawMessage{
		json.RawMessage(`{"changes":[{"listing_id":"p1","ok":true,"result":{"basket":{"id":"basket-9","lineItems":[{"quantity":1,"price":100,"totalPrice":100,"product":{"title":"Milk","listing":{"listingId":"p1"}}}],"summary":{"articleCount":1,"articlePrice":100,"totalPrice":100}}}}]}`),
		json.RawMessage(`{"changes":[{"listing_id":"p2","ok":false,"code":"upstream_changed"}]}`),
	}}
	gateway := fixedGateway(transport)
	got, err := gateway.ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{
		Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 1}, {ProductID: "p2", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	if len(got.Outcomes) != 2 {
		t.Fatalf("outcomes = %#v", got.Outcomes)
	}
	if got.Outcomes[0].Status != shopping.BasketChangeApplied {
		t.Fatalf("outcomes[0] = %#v", got.Outcomes[0])
	}
	if got.Outcomes[1].Status != shopping.BasketChangeRejected || got.Outcomes[1].Problem != shopping.BasketProblemUpstream {
		t.Fatalf("outcomes[1] = %#v", got.Outcomes[1])
	}
}

func TestGatewayApplyBasketPreservesPerItemAuthFailure(t *testing.T) {
	result := json.RawMessage(`{"changes":[{"listing_id":"p1","ok":false,"code":"auth_invalid"}]}`)
	gateway := fixedGateway(stubTransport{result: result})
	got, err := gateway.ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{
		Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	if got.Reconciled || got.ReconciliationProblem != "auth_invalid" || got.Outcomes[0].Status != shopping.BasketChangeRejected {
		t.Fatalf("ApplyBasket() = %#v", got)
	}
}

func TestGatewayApplyBasketMapsCanceledTransportErrorToAmbiguousResult(t *testing.T) {
	gateway := fixedGateway(stubTransport{err: codedStubError{code: "canceled"}})
	_, err := gateway.ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{
		Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 1}},
	})
	var ambiguousErr *shopping.AmbiguousResultError
	if !errors.As(err, &ambiguousErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewayApplyBasketMapsContentScriptUnreachableToAmbiguousResult(t *testing.T) {
	gateway := fixedGateway(stubTransport{err: codedStubError{code: "content_script_unreachable"}})
	_, err := gateway.ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{
		Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 1}},
	})
	var ambiguousErr *shopping.AmbiguousResultError
	if !errors.As(err, &ambiguousErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewayApplyBasketPreservesConfirmedOutcomesAfterLaterBridgeFailure(t *testing.T) {
	transport := &sequenceTransport{
		results: []json.RawMessage{json.RawMessage(`{"changes":[{"listing_id":"p1","ok":true,"result":{"basket":{"id":"basket-9","lineItems":[{"quantity":1,"price":100,"totalPrice":100,"product":{"title":"Milk","listing":{"listingId":"p1"}}}],"summary":{"articleCount":1,"articlePrice":100,"totalPrice":100}}}}],"basket":{"id":"basket-9","lineItems":[{"quantity":1,"price":100,"totalPrice":100,"product":{"title":"Milk","listing":{"listingId":"p1"}}}],"summary":{"articleCount":1,"articlePrice":100,"totalPrice":100}}}`)},
		errors:  []error{nil, codedStubError{code: "ambiguous_result"}, codedStubError{code: "bridge_unavailable"}},
	}
	got, err := fixedGateway(transport).ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 1}, {ProductID: "p2", Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outcomes) != 2 || got.Outcomes[0].Status != shopping.BasketChangeApplied || got.Outcomes[1].Status != shopping.BasketChangeUnknown {
		t.Fatalf("partial outcomes were not preserved: %#v", got)
	}
}

// routedTransport answers a different result per Operation — stubTransport
// can't, since it returns one fixed result regardless of which operation was
// requested, and this test needs ApplyBasket's own call and its fallback
// GetBasket call to see different responses.
type routedTransport struct {
	byOperation map[browserbridge.Operation]json.RawMessage
}

func (t routedTransport) Do(_ context.Context, op browserbridge.Operation, _ json.RawMessage) (json.RawMessage, error) {
	result, ok := t.byOperation[op]
	if !ok {
		return nil, errors.New("unexpected operation: " + string(op))
	}
	return result, nil
}

func TestGatewayApplyBasketFetchesAuthoritativeBasketAfterADeleteOnlyMutation(t *testing.T) {
	// A DELETE's 204 leaves content-script.js's per-item "result" empty, so
	// ApplyBasket alone never learns the post-mutation basket. It must fall
	// back to GetBasket using the basket ID it was already bound to.
	applyResult := json.RawMessage(`{"changes":[{"listing_id":"p1","ok":true,"result":null}]}`)
	basketResult := json.RawMessage(`{"basket":{"id":"basket-9","serviceSelection":{"wwIdent":"660500"},"lineItems":[],"summary":{"articleCount":0,"articlePrice":0,"totalPrice":0}}}`)
	transport := routedTransport{byOperation: map[browserbridge.Operation]json.RawMessage{
		browserbridge.OperationBasketApply: applyResult,
		browserbridge.OperationBasketGet:   basketResult,
	}}
	gateway := fixedGateway(transport)
	got, err := gateway.ApplyBasket(t.Context(), shopping.ShoppingContext{BasketID: "basket-9"}, shopping.BasketMutation{
		Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 0}},
	})
	if err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	if got.Basket.ID != "basket-9" {
		t.Fatalf("ApplyBasket() did not fall back to the authoritative basket: %#v", got.Basket)
	}
}

func TestGatewayApplyBasketReturnsEmptyBasketWhenNoBasketIDIsKnown(t *testing.T) {
	// Nothing to fall back to (no prior basket ID, no inline snapshot) — an
	// empty Basket is correct here, not an error: every requested change
	// legitimately failed (validated below), so there is nothing to report.
	applyResult := json.RawMessage(`{"changes":[{"listing_id":"p1","ok":false,"code":"invalid_params"}]}`)
	gateway := fixedGateway(stubTransport{result: applyResult})
	got, err := gateway.ApplyBasket(t.Context(), shopping.ShoppingContext{}, shopping.BasketMutation{
		Changes: []shopping.BasketChange{{ProductID: "p1", Quantity: 0}},
	})
	if err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	if got.Basket.ID != "" {
		t.Fatalf("ApplyBasket() fabricated a basket with no source: %#v", got.Basket)
	}
}

// --- ListTimeSlots ---

func TestGatewayListTimeSlotsDecodesFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/timeslots.sanitized.json")
	if err != nil {
		t.Fatalf("read sanitized fixture: %v", err)
	}
	gateway := fixedGateway(stubTransport{result: json.RawMessage(fixture)})
	list, err := gateway.ListTimeSlots(t.Context(), shopping.ShoppingContext{StoreID: "660500", PostalCode: "10115"})
	if err != nil {
		t.Fatalf("ListTimeSlots() error = %v", err)
	}
	if len(list.TimeSlots) != 1 || list.TimeSlots[0].ID != "timeslot-1" || list.TimeSlots[0].StoreID != "660500" {
		t.Fatalf("ListTimeSlots() = %#v", list)
	}
	if !list.ObservedAt.Equal(fixedNow) {
		t.Fatalf("ObservedAt = %v, want %v", list.ObservedAt, fixedNow)
	}
}

func TestGatewayListTimeSlotsMapsCodedErrors(t *testing.T) {
	tests := []struct {
		name string
		code string
		as   func(error) bool
	}{
		{"auth invalid", "auth_invalid", func(err error) bool { var target *shopping.AuthError; return errors.As(err, &target) }},
		{"rate limited", "rate_limited", func(err error) bool { var target *shopping.RateLimitError; return errors.As(err, &target) }},
		{"canceled", "canceled", func(err error) bool { var target *shopping.BridgeUnavailableError; return errors.As(err, &target) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gateway := fixedGateway(stubTransport{err: codedStubError{code: test.code}})
			_, err := gateway.ListTimeSlots(t.Context(), shopping.ShoppingContext{StoreID: "660500", PostalCode: "10115"})
			if !test.as(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGatewayListTimeSlotsFailsClosedWithNoStoreSelected(t *testing.T) {
	transport := &countingTransport{}
	_, err := fixedGateway(transport).ListTimeSlots(t.Context(), shopping.ShoppingContext{})
	var validationErr *shopping.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "store_id" {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport.calls != 0 {
		t.Fatalf("ListTimeSlots called the transport with no store selected: calls=%d", transport.calls)
	}
}

// --- SelectTimeSlot ---

type scriptedBridgeCall struct {
	operation browserbridge.Operation
	result    json.RawMessage
	err       error
}

type scriptedBridgeTransport struct {
	t      *testing.T
	calls  []scriptedBridgeCall
	params []json.RawMessage
}

func (t *scriptedBridgeTransport) Do(_ context.Context, operation browserbridge.Operation, params json.RawMessage) (json.RawMessage, error) {
	t.t.Helper()
	if len(t.params) >= len(t.calls) {
		t.t.Fatalf("unexpected operation: %s", operation)
	}
	call := t.calls[len(t.params)]
	if operation != call.operation {
		t.t.Fatalf("operation = %s, want %s", operation, call.operation)
	}
	t.params = append(t.params, append(json.RawMessage(nil), params...))
	return call.result, call.err
}

var availableSlot = json.RawMessage(`[{"id":"slot-1","startTime":"2026-08-20T08:00:00+02:00","endTime":"2026-08-20T09:00:00+02:00","serviceFee":0,"selected":false}]`)
var selectedSlot = json.RawMessage(`[{"id":"slot-1","startTime":"2026-08-20T08:00:00+02:00","endTime":"2026-08-20T09:00:00+02:00","serviceFee":0,"selected":true}]`)

func TestGatewaySelectTimeSlotReturnsAnAlreadySelectedSlotWithoutMutation(t *testing.T) {
	transport := &scriptedBridgeTransport{t: t, calls: []scriptedBridgeCall{{operation: browserbridge.OperationTimeslotsList, result: selectedSlot}}}
	current := shopping.ShoppingContext{StoreID: "660500", PostalCode: "10115"}
	next, err := fixedGateway(transport).SelectTimeSlot(t.Context(), current, "slot-1")
	if err != nil || next.TimeSlotID != "slot-1" {
		t.Fatalf("SelectTimeSlot() = %#v, %v", next, err)
	}
	if len(transport.params) != 1 {
		t.Fatalf("transport calls = %d, want one read", len(transport.params))
	}
}

func TestGatewaySelectTimeSlotReconcilesAnAmbiguousReservation(t *testing.T) {
	transport := &scriptedBridgeTransport{t: t, calls: []scriptedBridgeCall{
		{operation: browserbridge.OperationTimeslotsList, result: availableSlot},
		{operation: browserbridge.OperationTimeslotReserve, err: codedStubError{code: "ambiguous_result"}},
		{operation: browserbridge.OperationTimeslotsList, result: selectedSlot},
	}}
	current := shopping.ShoppingContext{StoreID: "660500", PostalCode: "10115"}
	next, err := fixedGateway(transport).SelectTimeSlot(t.Context(), current, "slot-1")
	if err != nil || next.TimeSlotID != "slot-1" {
		t.Fatalf("SelectTimeSlot() = %#v, %v", next, err)
	}
	var params timeslotReserveParams
	if err := json.Unmarshal(transport.params[1], &params); err != nil {
		t.Fatal(err)
	}
	if params.SlotID != "slot-1" || string(transport.params[1]) != `{"slot_id":"slot-1"}` {
		t.Fatalf("reservation params = %#v", params)
	}
}

func TestGatewaySelectTimeSlotReportsConfirmedFailure(t *testing.T) {
	transport := &scriptedBridgeTransport{t: t, calls: []scriptedBridgeCall{
		{operation: browserbridge.OperationTimeslotsList, result: availableSlot},
		{operation: browserbridge.OperationTimeslotReserve, err: codedStubError{code: "upstream_changed"}},
		{operation: browserbridge.OperationTimeslotsList, result: availableSlot},
	}}
	_, err := fixedGateway(transport).SelectTimeSlot(t.Context(), shopping.ShoppingContext{StoreID: "660500", PostalCode: "10115"}, "slot-1")
	var upstreamErr *shopping.UpstreamChangeError
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGatewaySelectTimeSlotReportsUnknownWhenReconciliationFails(t *testing.T) {
	transport := &scriptedBridgeTransport{t: t, calls: []scriptedBridgeCall{
		{operation: browserbridge.OperationTimeslotsList, result: availableSlot},
		{operation: browserbridge.OperationTimeslotReserve},
		{operation: browserbridge.OperationTimeslotsList, err: codedStubError{code: "bridge_unavailable"}},
	}}
	_, err := fixedGateway(transport).SelectTimeSlot(t.Context(), shopping.ShoppingContext{StoreID: "660500", PostalCode: "10115"}, "slot-1")
	var ambiguousErr *shopping.AmbiguousResultError
	if !errors.As(err, &ambiguousErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

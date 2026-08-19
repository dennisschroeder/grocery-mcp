package shopping_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

func TestGatewayErrorsAreDistinct(t *testing.T) {
	tests := []struct {
		name string
		err  error
		as   func(error) bool
	}{
		{"auth", &shopping.AuthError{Operation: "session.validate"}, func(err error) bool { var target *shopping.AuthError; return errors.As(err, &target) }},
		{"bridge unavailable", &shopping.BridgeUnavailableError{Operation: "session.refresh"}, func(err error) bool { var target *shopping.BridgeUnavailableError; return errors.As(err, &target) }},
		{"rate limit", &shopping.RateLimitError{Operation: "products.search", RetryAfter: time.Second}, func(err error) bool { var target *shopping.RateLimitError; return errors.As(err, &target) }},
		{"validation", &shopping.ValidationError{Operation: "basket.apply", Field: "quantity", Problem: shopping.ValidationInvalid}, func(err error) bool { var target *shopping.ValidationError; return errors.As(err, &target) }},
		{"upstream change", &shopping.UpstreamChangeError{Operation: "basket.get", Problem: shopping.UpstreamIncompatiblePayload}, func(err error) bool { var target *shopping.UpstreamChangeError; return errors.As(err, &target) }},
		{"ambiguous result", &shopping.AmbiguousResultError{Operation: "basket.apply", OperationID: "operation-1"}, func(err error) bool { var target *shopping.AmbiguousResultError; return errors.As(err, &target) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !test.as(test.err) {
				t.Fatalf("%T did not preserve its error type", test.err)
			}
		})
	}
}

func TestGatewayErrorsDoNotRenderSensitiveCauses(t *testing.T) {
	err := &shopping.UpstreamChangeError{
		Operation: "orders.list",
		Problem:   shopping.UpstreamMalformedJSON,
	}

	if got, want := err.Error(), "orders.list: upstream response changed (malformed_json)"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

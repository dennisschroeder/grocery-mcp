package rewe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type stubTransport struct {
	result json.RawMessage
	err    error
}

func (t stubTransport) Do(context.Context, browserbridge.Operation, json.RawMessage) (json.RawMessage, error) {
	return t.result, t.err
}

type codedStubError struct {
	code string
}

func (e codedStubError) Error() string { return "stub transport error" }
func (e codedStubError) Code() string  { return e.code }

func TestBrowserValidatorBindsIdentityFromFavorites(t *testing.T) {
	observedAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	validator := BrowserValidator{
		Transport: stubTransport{result: json.RawMessage(`[{"id":"list-123","name":"stub","items":[]}]`)},
		Now:       func() time.Time { return observedAt },
	}
	identity, err := validator.ValidateSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if identity.AccountID == "" || identity.ShopSessionID != "" || identity.ObservedAt != observedAt {
		t.Fatalf("unexpected identity: %#v", identity)
	}
}

func TestBrowserValidatorMapsAuthInvalidToAuthError(t *testing.T) {
	validator := BrowserValidator{Transport: stubTransport{err: codedStubError{code: "auth_invalid"}}}
	_, err := validator.ValidateSession(t.Context())
	var target *shopping.AuthError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserValidatorMapsRateLimitedToRateLimitError(t *testing.T) {
	validator := BrowserValidator{Transport: stubTransport{err: codedStubError{code: "rate_limited"}}}
	_, err := validator.ValidateSession(t.Context())
	var target *shopping.RateLimitError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserValidatorMapsContentScriptUnreachableToBridgeUnavailable(t *testing.T) {
	validator := BrowserValidator{Transport: stubTransport{err: codedStubError{code: "content_script_unreachable"}}}
	_, err := validator.ValidateSession(t.Context())
	var target *shopping.BridgeUnavailableError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserValidatorMapsCanceledToBridgeUnavailable(t *testing.T) {
	validator := BrowserValidator{Transport: stubTransport{err: codedStubError{code: "canceled"}}}
	_, err := validator.ValidateSession(t.Context())
	var target *shopping.BridgeUnavailableError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserValidatorClassifiesOtherTransportFailures(t *testing.T) {
	validator := BrowserValidator{Transport: stubTransport{err: codedStubError{code: "not_implemented"}}}
	_, err := validator.ValidateSession(t.Context())
	var target *shopping.UpstreamChangeError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserValidatorClassifiesUncodedTransportFailures(t *testing.T) {
	validator := BrowserValidator{Transport: stubTransport{err: errors.New("dial bridge socket: connection refused")}}
	_, err := validator.ValidateSession(t.Context())
	var target *shopping.UpstreamChangeError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserValidatorClassifiesMalformedFavoritesPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed", `not-json`},
		{"wrong top-level type", `{}`},
		{"empty list", `[]`},
		{"non-object list item", `["nope"]`},
		{"missing id", `[{}]`},
		{"id wrong type", `[{"id":123}]`},
		{"empty id", `[{"id":""}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := BrowserValidator{Transport: stubTransport{result: json.RawMessage(test.body)}}
			_, err := validator.ValidateSession(t.Context())
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

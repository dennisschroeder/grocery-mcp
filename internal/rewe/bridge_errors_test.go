package rewe

import (
	"errors"
	"testing"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

func TestBridgeTransportFailureClassification(t *testing.T) {
	for _, code := range []string{"content_script_unreachable", "operation_timeout", "canceled", "ambiguous_result"} {
		t.Run("ambiguous mutation "+code, func(t *testing.T) {
			err := codedStubError{code: code}
			var ambiguous *shopping.AmbiguousResultError
			if mapped := classifyMutationBridgeError("mutate", err); !errors.As(mapped, &ambiguous) {
				t.Fatalf("mutation mapping = %T, want AmbiguousResultError", mapped)
			}
		})
	}

	for _, code := range []string{"content_script_unreachable", "operation_timeout", "queue_busy", "canceled", "ambiguous_result", "not_dispatched", "bridge_unavailable"} {
		t.Run("unavailable read "+code, func(t *testing.T) {
			err := codedStubError{code: code}
			var unavailable *shopping.BridgeUnavailableError
			mapped := classifyReadBridgeError("read", err)
			if !errors.As(mapped, &unavailable) {
				t.Fatalf("read mapping = %T, want BridgeUnavailableError", mapped)
			}
			wantAction := code == "bridge_unavailable" || code == "not_dispatched" || code == "content_script_unreachable"
			if unavailable.ActionRequired != wantAction {
				t.Fatalf("ActionRequired = %t for %s", unavailable.ActionRequired, code)
			}
			unavailable = nil
			_, validatorErr := (BrowserValidator{Transport: stubTransport{err: err}}).ValidateSession(t.Context())
			if !errors.As(validatorErr, &unavailable) {
				t.Fatalf("validator mapping = %T, want BridgeUnavailableError", validatorErr)
			}
			if unavailable.ActionRequired != wantAction {
				t.Fatalf("validator ActionRequired = %t for %s", unavailable.ActionRequired, code)
			}
		})
	}

	var unavailable *shopping.BridgeUnavailableError
	if mapped := classifyMutationBridgeError("mutate", codedStubError{code: "bridge_unavailable"}); !errors.As(mapped, &unavailable) {
		t.Fatalf("pre-send mutation mapping = %T, want BridgeUnavailableError", mapped)
	}
	unavailable = nil
	if mapped := classifyMutationBridgeError("mutate", codedStubError{code: "not_dispatched"}); !errors.As(mapped, &unavailable) {
		t.Fatalf("undispatched mutation mapping = %T, want BridgeUnavailableError", mapped)
	}
	unavailable = nil
	if mapped := classifyMutationBridgeError("mutate", codedStubError{code: "queue_busy"}); !errors.As(mapped, &unavailable) {
		t.Fatalf("queued mutation mapping = %T, want BridgeUnavailableError", mapped)
	}
}

package rewe

import (
	"errors"
	"testing"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

func TestBridgeTransportFailureClassification(t *testing.T) {
	for _, code := range []string{"content_script_unreachable", "canceled", "ambiguous_result"} {
		t.Run("ambiguous mutation "+code, func(t *testing.T) {
			err := codedStubError{code: code}
			var ambiguous *shopping.AmbiguousResultError
			if mapped := classifyMutationBridgeError("mutate", err); !errors.As(mapped, &ambiguous) {
				t.Fatalf("mutation mapping = %T, want AmbiguousResultError", mapped)
			}
		})
	}

	for _, code := range []string{"content_script_unreachable", "canceled", "ambiguous_result", "bridge_unavailable"} {
		t.Run("unavailable read "+code, func(t *testing.T) {
			err := codedStubError{code: code}
			var unavailable *shopping.BridgeUnavailableError
			if mapped := classifyReadBridgeError("read", err); !errors.As(mapped, &unavailable) {
				t.Fatalf("read mapping = %T, want BridgeUnavailableError", mapped)
			}
			_, validatorErr := (BrowserValidator{Transport: stubTransport{err: err}}).ValidateSession(t.Context())
			if !errors.As(validatorErr, &unavailable) {
				t.Fatalf("validator mapping = %T, want BridgeUnavailableError", validatorErr)
			}
		})
	}

	var unavailable *shopping.BridgeUnavailableError
	if mapped := classifyMutationBridgeError("mutate", codedStubError{code: "bridge_unavailable"}); !errors.As(mapped, &unavailable) {
		t.Fatalf("pre-send mutation mapping = %T, want BridgeUnavailableError", mapped)
	}
}

package rewe

import (
	"errors"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// classifyReadBridgeError is shared by BrowserValidator and all read gateways.
// Reads may safely treat a canceled or unreachable bridge call as unavailable,
// since the caller can retry the read.
func classifyReadBridgeError(operation string, err error) error {
	var coded browserbridge.CodedError
	if errors.As(err, &coded) {
		switch coded.Code() {
		case "auth_invalid":
			return &shopping.AuthError{Operation: operation}
		case "rate_limited":
			return &shopping.RateLimitError{Operation: operation}
		case "content_script_unreachable", "canceled", "ambiguous_result", "bridge_unavailable":
			return &shopping.BridgeUnavailableError{Operation: operation}
		case "invalid_params":
			return &shopping.ValidationError{Operation: operation, Field: "params", Problem: shopping.ValidationInvalid}
		}
	}
	return &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamUnexpectedStatus}
}

// classifyMutationBridgeError treats canceled, ambiguous_result, and
// content_script_unreachable as ambiguous because the underlying REWE
// POST/DELETE may already have been dispatched. A mutation must not report
// those as safe-to-retry BridgeUnavailableError, per AGENTS.md ("mutations
// require proven idempotency or reconciliation"). bridge_unavailable is
// different: it is emitted only when no request bytes were sent. Only
// gateway_basket.go uses this today (basket_apply, timeslot_reserve); no
// other vertical has a mutation yet.
func classifyMutationBridgeError(operation string, err error) error {
	var coded browserbridge.CodedError
	if errors.As(err, &coded) {
		switch coded.Code() {
		case "auth_invalid":
			return &shopping.AuthError{Operation: operation}
		case "rate_limited":
			return &shopping.RateLimitError{Operation: operation}
		case "bridge_unavailable":
			return &shopping.BridgeUnavailableError{Operation: operation}
		case "content_script_unreachable", "canceled", "ambiguous_result":
			return &shopping.AmbiguousResultError{Operation: operation}
		case "invalid_params":
			return &shopping.ValidationError{Operation: operation, Field: "params", Problem: shopping.ValidationInvalid}
		}
	}
	return &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamUnexpectedStatus}
}

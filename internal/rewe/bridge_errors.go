package rewe

import (
	"errors"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// classifyReadBridgeError mirrors BrowserValidator.ValidateSession's coded-
// error mapping: reads may safely treat a canceled or unreachable bridge
// call as merely unavailable, since ReadWithRefresh's caller can just retry
// the read. Shared across all three Phase 1 verticals — each independently
// duplicated a near-identical classifier in its own isolated worktree
// (gateway_stores.go's mapBridgeError, gateway_orders.go's
// mapTransportError, gateway_basket.go's classifyReadBridgeError); this is
// the review-consolidated version.
func classifyReadBridgeError(operation string, err error) error {
	var coded browserbridge.CodedError
	if errors.As(err, &coded) {
		switch coded.Code() {
		case "auth_invalid":
			return &shopping.AuthError{Operation: operation}
		case "rate_limited":
			return &shopping.RateLimitError{Operation: operation}
		case "content_script_unreachable", "canceled":
			return &shopping.BridgeUnavailableError{Operation: operation}
		case "invalid_params":
			return &shopping.ValidationError{Operation: operation, Field: "params", Problem: shopping.ValidationInvalid}
		}
	}
	return &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamUnexpectedStatus}
}

// classifyMutationBridgeError differs from classifyReadBridgeError on
// exactly one code: "canceled" means the Go-side context gave up while
// waiting for the content script's reply, but the underlying REWE
// POST/DELETE may already have been dispatched — a mutation must not
// report that as a safe-to-retry BridgeUnavailableError, per AGENTS.md
// ("mutations require proven idempotency or reconciliation"). Only
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
		case "content_script_unreachable":
			return &shopping.BridgeUnavailableError{Operation: operation}
		case "canceled":
			return &shopping.AmbiguousResultError{Operation: operation}
		case "invalid_params":
			return &shopping.ValidationError{Operation: operation, Field: "params", Problem: shopping.ValidationInvalid}
		}
	}
	return &shopping.UpstreamChangeError{Operation: operation, Problem: shopping.UpstreamUnexpectedStatus}
}

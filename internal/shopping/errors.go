package shopping

import (
	"fmt"
	"time"
)

type ValidationProblem string

const (
	ValidationMissing  ValidationProblem = "missing"
	ValidationInvalid  ValidationProblem = "invalid"
	ValidationConflict ValidationProblem = "conflict"
)

type UpstreamProblem string

const (
	UpstreamMalformedJSON        UpstreamProblem = "malformed_json"
	UpstreamTypeChanged          UpstreamProblem = "type_changed"
	UpstreamMissingCriticalField UpstreamProblem = "missing_critical_field"
	UpstreamIncompatiblePayload  UpstreamProblem = "incompatible_payload"
	UpstreamTrailingPayload      UpstreamProblem = "trailing_payload"
	UpstreamUnexpectedStatus     UpstreamProblem = "unexpected_status"
)

type AuthError struct {
	Operation string
}

func (e *AuthError) Error() string {
	return operationError(e.Operation, "authentication failed")
}

type BridgeUnavailableError struct {
	Operation string
}

func (e *BridgeUnavailableError) Error() string {
	return operationError(e.Operation, "browser bridge is unavailable")
}

type RateLimitError struct {
	Operation  string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return operationError(e.Operation, "rate limited")
}

type ValidationError struct {
	Operation string
	Field     string
	Problem   ValidationProblem
}

func (e *ValidationError) Error() string {
	return operationError(e.Operation, fmt.Sprintf("validation failed for %s (%s)", e.Field, e.Problem))
}

type UpstreamChangeError struct {
	Operation string
	Problem   UpstreamProblem
}

func (e *UpstreamChangeError) Error() string {
	return operationError(e.Operation, fmt.Sprintf("upstream response changed (%s)", e.Problem))
}

type AmbiguousResultError struct {
	Operation   string
	OperationID string
}

func (e *AmbiguousResultError) Error() string {
	return operationError(e.Operation, "result is ambiguous and requires reconciliation")
}

func operationError(operation, message string) string {
	if operation == "" {
		return message
	}
	return operation + ": " + message
}

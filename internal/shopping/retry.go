package shopping

import (
	"context"
	"errors"
)

// SessionRefresher is the narrow capability a ReweGateway implementation
// needs to retry a read once after a successful browser-assisted refresh.
// *auth.Service satisfies this via RefreshAndValidate.
type SessionRefresher interface {
	RefreshAndValidate(context.Context) error
}

// ReadWithRefresh runs a read once. If it fails with an AuthError, it asks
// the session to refresh and, only on a successful refresh, retries the
// read exactly once — matching AGENTS.md: "Reads may retry once after a
// successful refresh." Any other error, or a failed refresh, returns
// immediately without a second attempt.
//
// Never use this for mutations: AGENTS.md requires proven idempotency or
// reconciliation before a mutation retry, which this helper does not
// provide — a second write attempt here could double-apply a change.
func ReadWithRefresh[T any](ctx context.Context, refresher SessionRefresher, read func(context.Context) (T, error)) (T, error) {
	result, err := read(ctx)
	if err == nil {
		return result, nil
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		return result, err
	}
	if refreshErr := refresher.RefreshAndValidate(ctx); refreshErr != nil {
		return result, err
	}
	return read(ctx)
}

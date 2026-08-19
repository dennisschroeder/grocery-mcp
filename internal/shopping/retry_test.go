package shopping

import (
	"context"
	"errors"
	"testing"
)

type stubRefresher struct {
	err   error
	calls int
}

func (r *stubRefresher) RefreshAndValidate(context.Context) error {
	r.calls++
	return r.err
}

func TestReadWithRefreshSkipsRefreshOnSuccess(t *testing.T) {
	refresher := &stubRefresher{}
	result, err := ReadWithRefresh(t.Context(), refresher, func(context.Context) (string, error) {
		return "ok", nil
	})
	if err != nil || result != "ok" {
		t.Fatalf("unexpected result: %q, %v", result, err)
	}
	if refresher.calls != 0 {
		t.Fatalf("refresh called %d times on success, want 0", refresher.calls)
	}
}

func TestReadWithRefreshSkipsRefreshOnNonAuthError(t *testing.T) {
	refresher := &stubRefresher{}
	wantErr := &RateLimitError{Operation: "test"}
	_, err := ReadWithRefresh(t.Context(), refresher, func(context.Context) (string, error) {
		return "", wantErr
	})
	if !errors.Is(err, error(wantErr)) {
		t.Fatalf("unexpected error: %v", err)
	}
	if refresher.calls != 0 {
		t.Fatalf("refresh called %d times on a non-auth error, want 0", refresher.calls)
	}
}

func TestReadWithRefreshRetriesExactlyOnceAfterSuccessfulRefresh(t *testing.T) {
	refresher := &stubRefresher{}
	attempts := 0
	result, err := ReadWithRefresh(t.Context(), refresher, func(context.Context) (string, error) {
		attempts++
		if attempts == 1 {
			return "", &AuthError{Operation: "test"}
		}
		return "ok", nil
	})
	if err != nil || result != "ok" {
		t.Fatalf("unexpected result: %q, %v", result, err)
	}
	if attempts != 2 {
		t.Fatalf("read attempted %d times, want 2", attempts)
	}
	if refresher.calls != 1 {
		t.Fatalf("refresh called %d times, want 1", refresher.calls)
	}
}

func TestReadWithRefreshReturnsOriginalErrorWhenRefreshFails(t *testing.T) {
	refresher := &stubRefresher{err: errors.New("refresh failed")}
	attempts := 0
	originalErr := &AuthError{Operation: "test"}
	_, err := ReadWithRefresh(t.Context(), refresher, func(context.Context) (string, error) {
		attempts++
		return "", originalErr
	})
	if !errors.Is(err, error(originalErr)) {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("read attempted %d times after a failed refresh, want 1", attempts)
	}
}

func TestReadWithRefreshDoesNotRetryTwice(t *testing.T) {
	refresher := &stubRefresher{}
	attempts := 0
	_, err := ReadWithRefresh(t.Context(), refresher, func(context.Context) (string, error) {
		attempts++
		return "", &AuthError{Operation: "test"}
	})
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("read attempted %d times, want exactly 2 (one retry)", attempts)
	}
	if refresher.calls != 1 {
		t.Fatalf("refresh called %d times, want 1", refresher.calls)
	}
}

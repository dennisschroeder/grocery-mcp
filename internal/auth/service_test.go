package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type validatorFunc func(context.Context) (shopping.SessionIdentity, error)

func (function validatorFunc) ValidateSession(ctx context.Context) (shopping.SessionIdentity, error) {
	return function(ctx)
}

func testBinding() *browserbridge.TabBinding {
	return browserbridge.NewTabBinding(time.Now())
}

func TestConnectCannotBecomeActiveBeforeValidation(t *testing.T) {
	service := NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		return shopping.SessionIdentity{}, errors.New("not called")
	}))
	status := service.Connect()
	if status.State != StateBootstrapping || !status.ActionRequired {
		t.Fatalf("unexpected status: %#v", status)
	}
	if _, ok := service.Identity(); ok {
		t.Fatal("identity available before validation")
	}
}

func TestAcceptActivatesOnlyWithBoundIdentity(t *testing.T) {
	observedAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	service := NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	service.random = strings.NewReader("0123456789abcdef")
	service.Connect()
	if err := service.Accept(t.Context(), testBinding()); err != nil {
		t.Fatal(err)
	}
	if service.Status().State != StateActive {
		t.Fatalf("state is %s", service.Status().State)
	}
	identity, ok := service.Identity()
	if !ok || identity.AccountID != "account" || identity.ShopSessionID != "session_30313233343536373839616263646566" {
		t.Fatalf("unexpected identity: %#v", identity)
	}
	if service.Disconnect().State != StateUnauthenticated {
		t.Fatal("disconnect did not clear the session")
	}
}

func TestRefreshPreservesBoundSessionWithinIdleWindow(t *testing.T) {
	current := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	service := NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: current}, nil
	}))
	service.random = strings.NewReader("0123456789abcdef")
	service.now = func() time.Time { return current }
	service.Connect()
	if err := service.Accept(t.Context(), browserbridge.NewTabBinding(current)); err != nil {
		t.Fatal(err)
	}
	before, _ := service.Identity()
	current = current.Add(time.Minute)
	if status := service.Refresh(); status.State != StateRefreshing || status.ActionRequired {
		t.Fatalf("unexpected refresh status: %#v", status)
	}
	if err := service.Accept(t.Context(), browserbridge.NewTabBinding(current)); err != nil {
		t.Fatal(err)
	}
	after, ok := service.Identity()
	if !ok || after.AccountID != before.AccountID || after.ShopSessionID != before.ShopSessionID {
		t.Fatalf("refresh changed bound identity: before=%#v after=%#v", before, after)
	}
	if service.Status().SessionRevision != 2 {
		t.Fatalf("session revision is %d", service.Status().SessionRevision)
	}
}

func TestRefreshAndValidateRetriesAnActiveSession(t *testing.T) {
	current := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	service := NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: current}, nil
	}))
	service.random = strings.NewReader("0123456789abcdef")
	service.now = func() time.Time { return current }
	service.Connect()
	if err := service.Accept(t.Context(), browserbridge.NewTabBinding(current)); err != nil {
		t.Fatal(err)
	}
	before, _ := service.Identity()
	current = current.Add(time.Minute)
	if err := service.RefreshAndValidate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if service.Status().State != StateActive {
		t.Fatalf("unexpected state after refresh: %s", service.Status().State)
	}
	after, ok := service.Identity()
	if !ok || after.AccountID != before.AccountID {
		t.Fatalf("refresh changed bound identity: before=%#v after=%#v", before, after)
	}
}

func TestRefreshAndValidateNoOpOutsideActive(t *testing.T) {
	service := NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		return shopping.SessionIdentity{}, errors.New("validator should not be called")
	}))
	err := service.RefreshAndValidate(t.Context())
	var bridgeError *BridgeError
	if !errors.As(err, &bridgeError) || bridgeError.Code() != "not_requested" {
		t.Fatalf("unexpected error: %v", err)
	}
	if service.Status().State != StateUnauthenticated {
		t.Fatalf("RefreshAndValidate changed state outside Active: %s", service.Status().State)
	}
}

func TestRefreshAndValidatePropagatesReauthRequired(t *testing.T) {
	current := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	fail := false
	service := NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if fail {
			return shopping.SessionIdentity{}, &shopping.AuthError{Operation: "validate session"}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: current}, nil
	}))
	service.random = strings.NewReader("0123456789abcdef")
	service.now = func() time.Time { return current }
	service.Connect()
	if err := service.Accept(t.Context(), browserbridge.NewTabBinding(current)); err != nil {
		t.Fatal(err)
	}
	fail = true
	err := service.RefreshAndValidate(t.Context())
	var bridgeError *BridgeError
	if !errors.As(err, &bridgeError) || bridgeError.Code() != "reauth_required" {
		t.Fatalf("unexpected error: %v", err)
	}
	if service.Status().State != StateReauthRequired {
		t.Fatalf("unexpected state: %s", service.Status().State)
	}
}

func TestRefreshRejectsAnExpiredBinding(t *testing.T) {
	current := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	service := NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: current}, nil
	}))
	service.random = strings.NewReader("0123456789abcdef")
	service.now = func() time.Time { return current }
	service.Connect()
	if err := service.Accept(t.Context(), browserbridge.NewTabBinding(current)); err != nil {
		t.Fatal(err)
	}
	current = current.Add(maxBindingIdle + time.Minute)
	service.Refresh()
	err := service.Accept(t.Context(), browserbridge.NewTabBinding(current))
	var bridgeError *BridgeError
	if !errors.As(err, &bridgeError) || bridgeError.Code() != "context_mismatch" {
		t.Fatalf("unexpected error: %v", err)
	}
	if service.Status().State != StateFailed {
		t.Fatalf("state is %s", service.Status().State)
	}
}

func TestAcceptFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		state State
		code  string
	}{
		{name: "auth", err: &shopping.AuthError{}, state: StateReauthRequired, code: "reauth_required"},
		{name: "upstream", err: &shopping.UpstreamChangeError{}, state: StateFailed, code: "upstream_changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
				return shopping.SessionIdentity{}, test.err
			}))
			service.Connect()
			err := service.Accept(t.Context(), testBinding())
			var bridgeError *BridgeError
			if !errors.As(err, &bridgeError) || bridgeError.Code() != test.code {
				t.Fatalf("unexpected error: %v", err)
			}
			if service.Status().State != test.state {
				t.Fatalf("state is %s", service.Status().State)
			}
			if _, ok := service.Identity(); ok {
				t.Fatal("failed validation retained an identity")
			}
		})
	}
}

func TestAcceptRequiresConnect(t *testing.T) {
	service := NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		return shopping.SessionIdentity{}, nil
	}))
	err := service.Accept(t.Context(), testBinding())
	var bridgeError *BridgeError
	if !errors.As(err, &bridgeError) || bridgeError.Code() != "not_requested" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDisconnectCancelsAndAwaitsValidation(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	service := NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return shopping.SessionIdentity{}, ctx.Err()
	}))
	service.Connect()
	binding := testBinding()
	result := make(chan error, 1)
	go func() {
		result <- service.Accept(t.Context(), binding)
	}()
	<-started
	if status := service.Connect(); status.State != StateValidating {
		t.Fatalf("repeated connect changed in-flight state: %#v", status)
	}
	if status := service.Disconnect(); status.State != StateUnauthenticated {
		t.Fatalf("unexpected disconnect status: %#v", status)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("disconnect returned before validation stopped")
	}
	if err := <-result; err == nil {
		t.Fatal("superseded validation succeeded")
	}
	if !binding.Expired(time.Now(), time.Hour) {
		t.Fatal("superseded binding remained live")
	}
}

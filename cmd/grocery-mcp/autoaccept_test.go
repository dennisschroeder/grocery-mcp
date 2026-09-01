package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/auth"
	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type stubValidator struct {
	identity shopping.SessionIdentity
	err      error
}

func (v stubValidator) ValidateSession(context.Context) (shopping.SessionIdentity, error) {
	return v.identity, v.err
}

func waitForState(t *testing.T, service *auth.Service, want auth.State) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if service.Status().State == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for state %s, last was %s", want, service.Status().State)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestConnectDrivesAcceptWithoutAnExternalCaller(t *testing.T) {
	observedAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	service := auth.NewService(stubValidator{identity: shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}})
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)

	if status := authenticator.Connect(); status.ActionRequired {
		t.Fatalf("unexpected status after Connect: %#v", status)
	}
	waitForState(t, service, auth.StateActive)
}

func TestLiveAuthenticatorStartsValidationWithoutAnAuthConnectCall(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	service := auth.NewService(stubValidator{identity: shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}})

	startAutoAcceptingAuthenticator(t.Context(), service)

	waitForState(t, service, auth.StateActive)
}

func TestConnectDoesNotRequestActionWhileAutomaticValidationIsRunning(t *testing.T) {
	started := make(chan struct{})
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		close(started)
		<-ctx.Done()
		return shopping.SessionIdentity{}, ctx.Err()
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	status := authenticator.Connect()
	if status.ActionRequired {
		t.Fatalf("automatic validation requested premature human action: %#v", status)
	}
	<-started
}

func TestConnectRevalidatesAnActiveSession(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 2 {
			close(secondStarted)
			<-releaseSecond
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.Connect()
	waitForState(t, service, auth.StateActive)

	if status := authenticator.Connect(); status.State != shopping.AuthRefreshing {
		t.Fatalf("Connect() on Active = %#v, want Refreshing", status)
	}
	<-secondStarted
	close(releaseSecond)
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
}

func TestConnectQueuesRefreshWhileTheSuccessfulDriverIsExiting(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 9, 45, 0, 0, time.UTC)
	service := auth.NewService(stubValidator{identity: shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}})
	service.Connect()
	if err := service.Accept(t.Context(), browserbridge.NewTabBinding(observedAt)); err != nil {
		t.Fatal(err)
	}
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.running = true

	if status := authenticator.Connect(); status.State != shopping.AuthRefreshing {
		t.Fatalf("Connect() = %#v, want Refreshing", status)
	}
	authenticator.mu.Lock()
	restartPending := authenticator.restartPending
	authenticator.mu.Unlock()
	if !restartPending {
		t.Fatal("refresh was lost while the previous driver was exiting")
	}
}

func TestConnectDrivesAcceptOnceReconnectRetries(t *testing.T) {
	service := auth.NewService(stubValidator{err: errors.New("upstream unavailable")})
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)

	authenticator.Connect()
	waitForState(t, service, auth.StateFailed)

	authenticator.Connect()
	waitForState(t, service, auth.StateFailed)
	if status := service.Status(); status.State != auth.StateFailed {
		t.Fatalf("second connect did not retry validation: %#v", status)
	}
}

func TestDriveAcceptOnceDoesNotDoubleFireWhileInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	calls := make(chan struct{}, 4)
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		calls <- struct{}{}
		close(started)
		<-release
		return shopping.SessionIdentity{}, errors.New("stop")
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)

	authenticator.Connect()
	<-started
	authenticator.driveAcceptOnce()
	authenticator.driveAcceptOnce()
	close(release)
	waitForState(t, service, auth.StateFailed)

	if len(calls) != 1 {
		t.Fatalf("validator called %d times while one Accept was in flight, want 1", len(calls))
	}
}

func TestAutomaticValidationRetriesAfterAttemptTimeout(t *testing.T) {
	observedAt := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			<-ctx.Done()
			return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session"}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.retryInterval = time.Millisecond
	authenticator.validationTimeout = 10 * time.Millisecond

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
}

func TestUnresponsiveBridgeRetriesWithoutHumanAction(t *testing.T) {
	observedAt := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session"}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.retryInterval = time.Millisecond
	authenticator.actionDelay = 0

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
	if status := authenticator.Status(); status.ActionRequired {
		t.Fatalf("unresponsive bridge requested human action: %#v", status)
	}
}

func TestMissingBridgeRequestsHumanAction(t *testing.T) {
	service := auth.NewService(stubValidator{err: &shopping.BridgeUnavailableError{
		Operation:      "validate session",
		ActionRequired: true,
	}})
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.actionDelay = 0

	authenticator.Connect()
	deadline := time.After(2 * time.Second)
	for !authenticator.Status().ActionRequired {
		select {
		case <-deadline:
			t.Fatalf("missing bridge did not request human action: %#v", authenticator.Status())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestMissingBridgeRecoversWithoutAnotherConnectCall(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	var available atomic.Bool
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if !available.Load() {
			return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session", ActionRequired: true}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.retryInterval = time.Millisecond
	authenticator.actionDelay = 0

	authenticator.Connect()
	deadline := time.After(2 * time.Second)
	for !authenticator.Status().ActionRequired {
		select {
		case <-deadline:
			t.Fatalf("missing bridge did not request human action: %#v", authenticator.Status())
		case <-time.After(time.Millisecond):
		}
	}

	available.Store(true)
	waitForState(t, service, auth.StateActive)
}

func TestDisconnectWaitsForDriverBeforeReconnect(t *testing.T) {
	observedAt := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	started := make(chan struct{})
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return shopping.SessionIdentity{}, ctx.Err()
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)

	authenticator.Connect()
	<-started
	if status := authenticator.Disconnect(); status.State != auth.StateUnauthenticated {
		t.Fatalf("disconnect status = %#v", status)
	}
	authenticator.mu.Lock()
	running := authenticator.running
	authenticator.mu.Unlock()
	if running {
		t.Fatal("disconnect returned before the validation driver stopped")
	}

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
}

func TestConnectDuringDriverExitHandsOffToAnotherValidation(t *testing.T) {
	base := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	actionCheck := make(chan struct{})
	releaseActionCheck := make(chan struct{})
	var nowCalls atomic.Int32
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session", ActionRequired: true}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: base}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.actionDelay = time.Second
	authenticator.now = func() time.Time {
		if nowCalls.Add(1) == 3 {
			close(actionCheck)
			<-releaseActionCheck
			return base.Add(time.Second)
		}
		return base
	}

	authenticator.Connect()
	<-actionCheck
	if status := authenticator.Connect(); status.State != shopping.AuthBootstrapping {
		t.Fatalf("reconnect status = %#v", status)
	}
	close(releaseActionCheck)
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
}

func TestDisconnectCannotRestoreActionRequiredFromExitingDriver(t *testing.T) {
	base := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	actionCheck := make(chan struct{})
	releaseActionCheck := make(chan struct{})
	var nowCalls atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session", ActionRequired: true}
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.actionDelay = time.Second
	authenticator.now = func() time.Time {
		if nowCalls.Add(1) == 3 {
			close(actionCheck)
			<-releaseActionCheck
			return base.Add(time.Second)
		}
		return base
	}

	authenticator.Connect()
	<-actionCheck
	disconnected := make(chan shopping.AuthStatus, 1)
	go func() { disconnected <- authenticator.Disconnect() }()
	waitForState(t, service, auth.StateUnauthenticated)
	close(releaseActionCheck)
	if status := <-disconnected; status.State != shopping.AuthUnauthenticated {
		t.Fatalf("disconnect status = %#v", status)
	}
	if status := authenticator.Status(); status.ActionRequired {
		t.Fatalf("disconnect left action required: %#v", status)
	}
}

type validatorFunc func(context.Context) (shopping.SessionIdentity, error)

func (f validatorFunc) ValidateSession(ctx context.Context) (shopping.SessionIdentity, error) {
	return f(ctx)
}

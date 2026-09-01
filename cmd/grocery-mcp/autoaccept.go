package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/auth"
	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// autoAcceptingAuthenticator wraps *auth.Service for the live MCP server.
// Outcome B had an inbound push: the extension delivered a credential to a
// listening socket, and the server's connection handler called Accept
// directly as that message arrived. Outcome C inverted the direction — the
// native host polls the server for work — so nothing pushes into Accept
// anymore; something has to proactively drive validation once Connect enters
// Bootstrapping, the way bridge-smoke's CLI harness does manually. This type
// is that missing driver for the real server, with bounded attempts and
// automatic retries.
type autoAcceptingAuthenticator struct {
	ctx     context.Context
	service *auth.Service
	now     func() time.Time

	mu                sync.Mutex
	running           bool
	needsAction       bool
	generation        uint64
	driverCancel      context.CancelFunc
	driverDone        chan struct{}
	restartPending    bool
	retryInterval     time.Duration
	actionDelay       time.Duration
	validationTimeout time.Duration
}

const (
	automaticRetryInterval     = time.Second
	automaticActionDelay       = 30 * time.Second
	automaticValidationTimeout = 90 * time.Second
)

func newAutoAcceptingAuthenticator(ctx context.Context, service *auth.Service) *autoAcceptingAuthenticator {
	return &autoAcceptingAuthenticator{
		ctx:               ctx,
		service:           service,
		now:               time.Now,
		retryInterval:     automaticRetryInterval,
		actionDelay:       automaticActionDelay,
		validationTimeout: automaticValidationTimeout,
	}
}

func startAutoAcceptingAuthenticator(ctx context.Context, service *auth.Service) *autoAcceptingAuthenticator {
	authenticator := newAutoAcceptingAuthenticator(ctx, service)
	authenticator.Connect()
	return authenticator
}

func (a *autoAcceptingAuthenticator) Connect() shopping.AuthStatus {
	if a.service.Status().State == shopping.AuthActive {
		a.service.Refresh()
	} else {
		a.service.Connect()
	}
	a.driveAcceptOnce()
	return a.Status()
}

func (a *autoAcceptingAuthenticator) Status() shopping.AuthStatus {
	status := a.service.Status()
	a.mu.Lock()
	needsAction := a.needsAction
	a.mu.Unlock()
	status.ActionRequired = status.State == shopping.AuthReauthRequired || needsAction
	return status
}

func (a *autoAcceptingAuthenticator) Disconnect() shopping.AuthStatus {
	a.mu.Lock()
	a.generation++
	a.needsAction = false
	a.restartPending = false
	cancel := a.driverCancel
	done := a.driverDone
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	status := a.service.Disconnect()
	if done != nil {
		<-done
	}
	return status
}

func (a *autoAcceptingAuthenticator) Identity() (shopping.SessionIdentity, bool) {
	return a.service.Identity()
}

func (a *autoAcceptingAuthenticator) RefreshAndValidate(ctx context.Context) error {
	return a.service.RefreshAndValidate(ctx)
}

// driveAcceptOnce starts at most one in-flight Accept per Connect call.
// Accept itself is responsible for rejecting a stale attempt (via its
// requestRevision check) if a newer Connect/Disconnect superseded this one
// before the browser answered; the error is discarded here because the
// service's own state, not this goroutine's return value, is what auth_status
// reports.
func (a *autoAcceptingAuthenticator) driveAcceptOnce() {
	status := a.service.Status()
	a.mu.Lock()
	if a.running {
		if status.State == shopping.AuthBootstrapping || status.State == shopping.AuthRefreshing {
			a.restartPending = true
		}
		a.mu.Unlock()
		return
	}
	a.running = true
	a.needsAction = false
	a.generation++
	generation := a.generation
	driverContext, cancelDriver := context.WithCancel(a.ctx)
	done := make(chan struct{})
	a.driverCancel = cancelDriver
	a.driverDone = done
	a.mu.Unlock()

	go a.runAcceptDriver(driverContext, cancelDriver, done, generation)
}

func (a *autoAcceptingAuthenticator) runAcceptDriver(driverContext context.Context, cancelDriver context.CancelFunc, done chan struct{}, generation uint64) {
	for {
		a.runAcceptGeneration(driverContext, generation)

		a.mu.Lock()
		state := a.service.Status().State
		if state == shopping.AuthActive {
			a.needsAction = false
		}
		if a.restartPending && (state == shopping.AuthBootstrapping || state == shopping.AuthRefreshing) && driverContext.Err() == nil {
			a.restartPending = false
			a.needsAction = false
			a.generation++
			generation = a.generation
			a.mu.Unlock()
			continue
		}
		a.restartPending = false
		a.running = false
		if a.driverDone == done {
			a.driverCancel = nil
			a.driverDone = nil
		}
		close(done)
		a.mu.Unlock()
		cancelDriver()
		return
	}
}

func (a *autoAcceptingAuthenticator) runAcceptGeneration(driverContext context.Context, generation uint64) {
	started := a.now()
	for {
		a.mu.Lock()
		current := a.generation
		a.mu.Unlock()
		if current != generation {
			return
		}
		attemptContext, cancelAttempt := context.WithTimeout(driverContext, a.validationTimeout)
		err := a.service.Accept(attemptContext, browserbridge.NewTabBinding(a.now()))
		cancelAttempt()
		if err == nil || driverContext.Err() != nil {
			return
		}
		if status := a.service.Status(); status.State == shopping.AuthReauthRequired {
			return
		}
		var coded interface{ Code() string }
		missingPort := errors.As(err, &coded) && coded.Code() == "bridge_unavailable"
		if missingPort && a.now().Sub(started) >= a.actionDelay {
			a.mu.Lock()
			if a.generation == generation && driverContext.Err() == nil {
				a.needsAction = true
			}
			a.mu.Unlock()
		}
		timer := time.NewTimer(a.retryInterval)
		select {
		case <-timer.C:
			a.mu.Lock()
			current = a.generation
			a.mu.Unlock()
			if current != generation {
				return
			}
			a.service.Connect()
		case <-driverContext.Done():
			timer.Stop()
			return
		}
	}
}

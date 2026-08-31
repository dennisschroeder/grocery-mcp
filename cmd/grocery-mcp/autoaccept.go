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
// is that missing driver for the real server, one background attempt per
// Connect call.
type autoAcceptingAuthenticator struct {
	ctx     context.Context
	service *auth.Service
	now     func() time.Time

	mu          sync.Mutex
	running     bool
	needsAction bool
	generation  uint64
}

var automaticRetryInterval = time.Second
var automaticActionDelay = 30 * time.Second

func newAutoAcceptingAuthenticator(ctx context.Context, service *auth.Service) *autoAcceptingAuthenticator {
	return &autoAcceptingAuthenticator{ctx: ctx, service: service, now: time.Now}
}

func (a *autoAcceptingAuthenticator) Connect() shopping.AuthStatus {
	a.service.Connect()
	a.driveAcceptOnce()
	return a.Status()
}

func (a *autoAcceptingAuthenticator) Status() shopping.AuthStatus {
	status := a.service.Status()
	a.mu.Lock()
	running := a.running
	needsAction := a.needsAction
	a.mu.Unlock()
	status.ActionRequired = status.State == shopping.AuthReauthRequired || needsAction
	if running {
		status.ActionRequired = false
	}
	return status
}

func (a *autoAcceptingAuthenticator) Disconnect() shopping.AuthStatus {
	a.mu.Lock()
	a.generation++
	a.needsAction = false
	a.mu.Unlock()
	return a.service.Disconnect()
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
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return
	}
	a.running = true
	a.needsAction = false
	a.generation++
	generation := a.generation
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.running = false
			a.mu.Unlock()
		}()
		started := a.now()
		for {
			a.mu.Lock()
			current := a.generation
			a.mu.Unlock()
			if current != generation {
				return
			}
			err := a.service.Accept(a.ctx, browserbridge.NewTabBinding(a.now()))
			if err == nil || a.ctx.Err() != nil {
				return
			}
			if status := a.service.Status(); status.State == shopping.AuthReauthRequired {
				return
			}
			var coded interface{ Code() string }
			missingPort := errors.As(err, &coded) && coded.Code() == "bridge_unavailable"
			if missingPort && a.now().Sub(started) >= automaticActionDelay {
				a.mu.Lock()
				a.needsAction = true
				a.mu.Unlock()
				return
			}
			timer := time.NewTimer(automaticRetryInterval)
			select {
			case <-timer.C:
				a.mu.Lock()
				current = a.generation
				a.mu.Unlock()
				if current != generation {
					return
				}
				a.service.Connect()
			case <-a.ctx.Done():
				timer.Stop()
				return
			}
		}
	}()
}

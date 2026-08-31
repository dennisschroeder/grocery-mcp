package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type State = shopping.AuthState

const (
	StateUnauthenticated = shopping.AuthUnauthenticated
	StateBootstrapping   = shopping.AuthBootstrapping
	StateValidating      = shopping.AuthValidating
	StateActive          = shopping.AuthActive
	StateRefreshing      = shopping.AuthRefreshing
	StateReauthRequired  = shopping.AuthReauthRequired
	StateFailed          = shopping.AuthFailed
)

// maxBindingIdle bounds how long a previously accepted tab binding may sit
// untouched before a refresh can no longer trust it was still live. There is
// no secret to compare during a refresh (unlike the old cookie rotation
// check), so a bound-and-recent Touch is the freshness proof instead.
const maxBindingIdle = 10 * time.Minute

type Validator interface {
	ValidateSession(context.Context) (shopping.SessionIdentity, error)
}

type Status = shopping.AuthStatus

type Service struct {
	mu               sync.RWMutex
	validator        Validator
	now              func() time.Time
	random           io.Reader
	state            State
	requestRevision  uint64
	bindingRevision  uint64
	binding          *browserbridge.TabBinding
	identity         shopping.SessionIdentity
	validationCancel context.CancelFunc
	validationDone   chan struct{}
}

func NewService(validator Validator) *Service {
	return &Service{validator: validator, now: time.Now, random: rand.Reader, state: StateUnauthenticated}
}

func (s *Service) Connect() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case StateActive, StateBootstrapping, StateValidating, StateRefreshing:
		return s.statusLocked()
	default:
		s.requestRevision++
		s.clearLocked()
		s.state = StateBootstrapping
	}
	return s.statusLocked()
}

func (s *Service) Refresh() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateActive {
		s.requestRevision++
		s.state = StateRefreshing
	}
	return s.statusLocked()
}

func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statusLocked()
}

func (s *Service) Identity() (shopping.SessionIdentity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != StateActive {
		return shopping.SessionIdentity{}, false
	}
	return s.identity, true
}

func (s *Service) Accept(ctx context.Context, binding *browserbridge.TabBinding) error {
	if binding == nil {
		return &BridgeError{code: "invalid_binding"}
	}

	s.mu.Lock()
	refresh := s.state == StateRefreshing
	if s.state != StateBootstrapping && s.state != StateReauthRequired && !refresh {
		s.mu.Unlock()
		binding.Clear()
		return &BridgeError{code: "not_requested"}
	}
	requestRevision := s.requestRevision
	previousIdentity := s.identity
	previousBinding := s.binding
	s.state = StateValidating
	validator := s.validator
	if validator == nil {
		s.clearLocked()
		s.state = StateFailed
		s.mu.Unlock()
		binding.Clear()
		return &BridgeError{code: "validation_failed"}
	}
	validationContext, validationCancel := context.WithCancel(ctx)
	validationDone := make(chan struct{})
	s.validationCancel = validationCancel
	s.validationDone = validationDone
	s.mu.Unlock()

	defer close(validationDone)
	validated, err := validator.ValidateSession(validationContext)
	validationCancel()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.validationDone == validationDone {
		s.validationCancel = nil
		s.validationDone = nil
	}
	if requestRevision != s.requestRevision || s.state != StateValidating {
		binding.Clear()
		return &BridgeError{code: "request_superseded"}
	}
	if err != nil {
		binding.Clear()
		s.clearLocked()
		var authError *shopping.AuthError
		if errors.As(err, &authError) {
			s.state = StateReauthRequired
			return &BridgeError{code: "reauth_required"}
		}
		s.state = StateFailed
		var bridgeErr *shopping.BridgeUnavailableError
		if errors.As(err, &bridgeErr) {
			if !bridgeErr.ActionRequired {
				return &BridgeError{code: "bridge_unresponsive"}
			}
			return &BridgeError{code: "bridge_unavailable"}
		}
		var rateErr *shopping.RateLimitError
		if errors.As(err, &rateErr) {
			return &BridgeError{code: "rate_limited"}
		}
		var upstreamErr *shopping.UpstreamChangeError
		if errors.As(err, &upstreamErr) {
			return &BridgeError{code: "upstream_changed"}
		}
		return &BridgeError{code: "validation_failed"}
	}
	if validated.AccountID == "" || validated.ObservedAt.IsZero() {
		binding.Clear()
		s.clearLocked()
		s.state = StateFailed
		return &BridgeError{code: "validation_failed"}
	}

	if refresh {
		if previousIdentity.AccountID != validated.AccountID || previousBinding == nil || previousBinding.Expired(s.now(), maxBindingIdle) {
			binding.Clear()
			s.clearLocked()
			s.state = StateFailed
			return &BridgeError{code: "context_mismatch"}
		}
		previousBinding.Clear()
		s.identity = previousIdentity
		s.identity.ObservedAt = validated.ObservedAt
	} else {
		sessionID, err := newSessionID(s.random)
		if err != nil {
			binding.Clear()
			s.clearLocked()
			s.state = StateFailed
			return &BridgeError{code: "validation_failed"}
		}
		s.clearLocked()
		s.identity = validated
		s.identity.ShopSessionID = shopping.ShopSessionID(sessionID)
	}
	binding.Touch(s.now())
	s.binding = binding
	s.bindingRevision++
	s.state = StateActive
	return nil
}

// RefreshAndValidate is the synchronous counterpart to the live server's
// auto-accept driver (cmd/grocery-mcp/autoaccept.go), which only drives
// Connect. ReweGateway implementations call this after a read hits
// AuthError: it moves Active -> Refreshing and performs the resulting
// Accept synchronously, so the caller knows before retrying whether the
// refresh actually landed. A no-op (returns "not_requested") outside
// StateActive — there is nothing to refresh.
func (s *Service) RefreshAndValidate(ctx context.Context) error {
	if status := s.Refresh(); status.State != StateRefreshing {
		return &BridgeError{code: "not_requested"}
	}
	return s.Accept(ctx, browserbridge.NewTabBinding(s.now()))
}

func (s *Service) Disconnect() Status {
	s.mu.Lock()
	s.requestRevision++
	cancel := s.validationCancel
	done := s.validationDone
	if cancel != nil {
		cancel()
	}
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearLocked()
	s.state = StateUnauthenticated
	return s.statusLocked()
}

func (s *Service) statusLocked() Status {
	return Status{
		State:           s.state,
		ActionRequired:  s.state == StateBootstrapping || s.state == StateReauthRequired,
		SessionRevision: s.bindingRevision,
		ObservedAt:      s.now().UTC(),
	}
}

func (s *Service) clearLocked() {
	if s.binding != nil {
		s.binding.Clear()
		s.binding = nil
	}
	s.identity = shopping.SessionIdentity{}
	s.bindingRevision = 0
}

func newSessionID(random io.Reader) (string, error) {
	var data [16]byte
	if _, err := io.ReadFull(random, data[:]); err != nil {
		return "", err
	}
	return "session_" + hex.EncodeToString(data[:]), nil
}

type BridgeError struct {
	code string
}

func (e *BridgeError) Error() string {
	return "browser session was not accepted"
}

func (e *BridgeError) Code() string {
	return e.code
}

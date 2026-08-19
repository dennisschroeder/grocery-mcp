package shopping

import "context"

// stubAuthenticator is shared across this package's *_test.go files
// (core_basket_test.go, core_stores_test.go) — each vertical's isolated
// worktree wrote its own copy independently; consolidated at fan-in since
// Go doesn't allow two same-named types in one package.
type stubAuthenticator struct {
	identity     SessionIdentity
	hasIdentity  bool
	refreshErr   error
	refreshCalls int
}

func (s *stubAuthenticator) Connect() AuthStatus    { return AuthStatus{} }
func (s *stubAuthenticator) Status() AuthStatus     { return AuthStatus{} }
func (s *stubAuthenticator) Disconnect() AuthStatus { return AuthStatus{} }
func (s *stubAuthenticator) Identity() (SessionIdentity, bool) {
	return s.identity, s.hasIdentity
}
func (s *stubAuthenticator) RefreshAndValidate(context.Context) error {
	s.refreshCalls++
	return s.refreshErr
}

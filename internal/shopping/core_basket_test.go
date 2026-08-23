package shopping

import (
	"context"
	"errors"
	"testing"
)

// fakeBasketGateway is a narrow test double built for this vertical only —
// per the frozen contract, tests here must not wire a full Core with all
// three gateways, since stores/orders don't exist in this worktree.
// stubAuthenticator is shared across this package's tests — see
// authenticator_stub_test.go.
type fakeBasketGateway struct {
	getBasket      func(context.Context, ShoppingContext) (Basket, error)
	applyBasket    func(context.Context, ShoppingContext, BasketMutation) (BasketMutationResult, error)
	listTimeSlots  func(context.Context, ShoppingContext) (TimeSlotList, error)
	selectTimeSlot func(context.Context, ShoppingContext, TimeSlotID) (ShoppingContext, error)
}

func (f *fakeBasketGateway) GetBasket(ctx context.Context, sc ShoppingContext) (Basket, error) {
	return f.getBasket(ctx, sc)
}

func (f *fakeBasketGateway) ApplyBasket(ctx context.Context, sc ShoppingContext, mutation BasketMutation) (BasketMutationResult, error) {
	return f.applyBasket(ctx, sc, mutation)
}

func (f *fakeBasketGateway) ListTimeSlots(ctx context.Context, sc ShoppingContext) (TimeSlotList, error) {
	return f.listTimeSlots(ctx, sc)
}

func (f *fakeBasketGateway) SelectTimeSlot(ctx context.Context, sc ShoppingContext, id TimeSlotID) (ShoppingContext, error) {
	return f.selectTimeSlot(ctx, sc, id)
}

func TestCoreGetBasketFailsClosedWithoutIdentity(t *testing.T) {
	core := NewCore(&stubAuthenticator{}, nil, &fakeBasketGateway{}, nil, nil)
	_, err := core.GetBasket(t.Context())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCoreGetBasketReturnsGatewayResult(t *testing.T) {
	want := Basket{ID: "basket-1"}
	gateway := &fakeBasketGateway{
		getBasket: func(context.Context, ShoppingContext) (Basket, error) { return want, nil },
	}
	core := NewCore(&stubAuthenticator{hasIdentity: true}, nil, gateway, nil, nil)
	got, err := core.GetBasket(t.Context())
	if err != nil || got.ID != want.ID {
		t.Fatalf("GetBasket() = %#v, %v", got, err)
	}
}

func TestCoreGetBasketRetriesOnceAfterAuthErrorThenRefresh(t *testing.T) {
	attempts := 0
	gateway := &fakeBasketGateway{
		getBasket: func(context.Context, ShoppingContext) (Basket, error) {
			attempts++
			if attempts == 1 {
				return Basket{}, &AuthError{Operation: "get basket"}
			}
			return Basket{ID: "basket-1"}, nil
		},
	}
	auth := &stubAuthenticator{hasIdentity: true}
	core := NewCore(auth, nil, gateway, nil, nil)
	got, err := core.GetBasket(t.Context())
	if err != nil || got.ID != "basket-1" {
		t.Fatalf("GetBasket() = %#v, %v", got, err)
	}
	if attempts != 2 || auth.refreshCalls != 1 {
		t.Fatalf("attempts = %d, refreshCalls = %d, want 2 and 1", attempts, auth.refreshCalls)
	}
}

func TestCoreApplyBasketNeverRetriesOnAuthError(t *testing.T) {
	calls := 0
	gateway := &fakeBasketGateway{
		applyBasket: func(context.Context, ShoppingContext, BasketMutation) (BasketMutationResult, error) {
			calls++
			return BasketMutationResult{}, &AuthError{Operation: "apply basket"}
		},
	}
	auth := &stubAuthenticator{hasIdentity: true}
	core := NewCore(auth, nil, gateway, nil, nil)
	_, err := core.ApplyBasket(t.Context(), BasketMutation{Changes: []BasketChange{{ProductID: "p1", Quantity: 1}}})
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("gateway called %d times, want exactly 1 (never retried)", calls)
	}
	if auth.refreshCalls != 0 {
		t.Fatalf("refresh called %d times, want 0 for a mutation", auth.refreshCalls)
	}
}

func TestCoreApplyBasketReturnsPartialOutcomes(t *testing.T) {
	want := BasketMutationResult{
		Outcomes: []BasketChangeOutcome{
			{ProductID: "p1", RequestedQuantity: 2, AppliedQuantity: 2, Status: BasketChangeApplied},
			{ProductID: "p2", RequestedQuantity: 1, AppliedQuantity: 0, Status: BasketChangeRejected, Problem: BasketProblemUnknown},
		},
	}
	gateway := &fakeBasketGateway{
		applyBasket: func(context.Context, ShoppingContext, BasketMutation) (BasketMutationResult, error) { return want, nil },
	}
	core := NewCore(&stubAuthenticator{hasIdentity: true}, nil, gateway, nil, nil)
	got, err := core.ApplyBasket(t.Context(), BasketMutation{Changes: []BasketChange{{ProductID: "p1", Quantity: 2}, {ProductID: "p2", Quantity: 1}}})
	if err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	if len(got.Outcomes) != 2 || got.Outcomes[1].Status != BasketChangeRejected {
		t.Fatalf("ApplyBasket() = %#v", got)
	}
}

func TestCoreApplyBasketRebindsContextToDiscoveredBasketID(t *testing.T) {
	gateway := &fakeBasketGateway{
		applyBasket: func(context.Context, ShoppingContext, BasketMutation) (BasketMutationResult, error) {
			return BasketMutationResult{Basket: Basket{ID: "basket-42"}}, nil
		},
	}
	auth := &stubAuthenticator{hasIdentity: true, identity: SessionIdentity{AccountID: "account-1"}}
	core := NewCore(auth, nil, gateway, nil, nil)
	if _, err := core.ApplyBasket(t.Context(), BasketMutation{Changes: []BasketChange{{ProductID: "p1", Quantity: 1}}}); err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	if core.Context().BasketID != "basket-42" {
		t.Fatalf("Core did not rebind to the discovered basket ID: %#v", core.Context())
	}
}

func TestCoreApplyBasketDoesNotClearAnExistingBasketIDWhenResultHasNone(t *testing.T) {
	gateway := &fakeBasketGateway{
		applyBasket: func(context.Context, ShoppingContext, BasketMutation) (BasketMutationResult, error) {
			// A gateway that can't determine the authoritative basket (e.g. a
			// transport error mid-refresh) must not report an empty ID Core
			// would otherwise use to wipe a real binding — this stub proves
			// Core is defensive even if a gateway regresses on that.
			return BasketMutationResult{Basket: Basket{}}, nil
		},
	}
	auth := &stubAuthenticator{hasIdentity: true, identity: SessionIdentity{AccountID: "account-1"}}
	core := NewCore(auth, nil, gateway, nil, nil)
	core.rebindContext(ShoppingContext{AccountID: "account-1", BasketID: "basket-existing"})
	if _, err := core.ApplyBasket(t.Context(), BasketMutation{Changes: []BasketChange{{ProductID: "p1", Quantity: 0}}}); err != nil {
		t.Fatalf("ApplyBasket() error = %v", err)
	}
	if core.Context().BasketID != "basket-existing" {
		t.Fatalf("Core cleared an existing basket ID: %#v", core.Context())
	}
}

func TestCoreListTimeSlotsUsesReadWithRefresh(t *testing.T) {
	attempts := 0
	gateway := &fakeBasketGateway{
		listTimeSlots: func(context.Context, ShoppingContext) (TimeSlotList, error) {
			attempts++
			if attempts == 1 {
				return TimeSlotList{}, &AuthError{Operation: "list timeslots"}
			}
			return TimeSlotList{TimeSlots: []TimeSlot{{ID: "slot-1"}}}, nil
		},
	}
	auth := &stubAuthenticator{hasIdentity: true}
	core := NewCore(auth, nil, gateway, nil, nil)
	got, err := core.ListTimeSlots(t.Context())
	if err != nil || len(got.TimeSlots) != 1 {
		t.Fatalf("ListTimeSlots() = %#v, %v", got, err)
	}
	if auth.refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", auth.refreshCalls)
	}
}

func TestCoreSelectTimeSlotRebindsContextOnSuccess(t *testing.T) {
	next := ShoppingContext{AccountID: "account-1", StoreID: "store-1", BasketID: "basket-1", TimeSlotID: "slot-1"}
	gateway := &fakeBasketGateway{
		selectTimeSlot: func(context.Context, ShoppingContext, TimeSlotID) (ShoppingContext, error) { return next, nil },
	}
	core := NewCore(&stubAuthenticator{hasIdentity: true}, nil, gateway, nil, nil)
	got, err := core.SelectTimeSlot(t.Context(), "slot-1")
	if err != nil || got != next {
		t.Fatalf("SelectTimeSlot() = %#v, %v", got, err)
	}
	if core.Context() != next {
		t.Fatalf("Core.Context() = %#v, want %#v", core.Context(), next)
	}
}

func TestCoreSelectTimeSlotDoesNotRebindOnError(t *testing.T) {
	gateway := &fakeBasketGateway{
		selectTimeSlot: func(context.Context, ShoppingContext, TimeSlotID) (ShoppingContext, error) {
			// A real gateway attempting a new binding on failure would be a
			// bug; returning one here proves Core ignores it on error.
			return ShoppingContext{AccountID: "account-1", TimeSlotID: "slot-should-not-stick"}, &ValidationError{
				Operation: "select timeslot", Field: "customer_id", Problem: ValidationMissing,
			}
		},
	}
	auth := &stubAuthenticator{hasIdentity: true, identity: SessionIdentity{AccountID: "account-1"}}
	core := NewCore(auth, nil, gateway, nil, nil)
	_, err := core.SelectTimeSlot(t.Context(), "slot-1")
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	// boundContext() still ran (accounts for the auth binding), but the
	// gateway's returned context must never be installed on failure.
	want := ShoppingContext{AccountID: "account-1"}
	if core.Context() != want {
		t.Fatalf("Core.Context() = %#v, want %#v", core.Context(), want)
	}
}

package shopping

import (
	"context"
	"errors"
	"testing"
)

// fakeCheckoutGate and fakeCheckoutBasketGateway are narrow test doubles
// built for this vertical only — per the frozen contract, tests here must
// not wire a full Core with every gateway.
type fakeCheckoutGate struct {
	prepare func(context.Context, Basket, StoreID, TimeSlotID) (CheckoutApproval, error)
	status  func(context.Context, ApprovalID, Basket) (CheckoutApproval, error)
}

func (f *fakeCheckoutGate) Prepare(ctx context.Context, basket Basket, storeID StoreID, timeSlotID TimeSlotID) (CheckoutApproval, error) {
	return f.prepare(ctx, basket, storeID, timeSlotID)
}

func (f *fakeCheckoutGate) Status(ctx context.Context, id ApprovalID, current Basket) (CheckoutApproval, error) {
	return f.status(ctx, id, current)
}

type fakeCheckoutBasketGateway struct {
	basket Basket
	err    error
}

func (f *fakeCheckoutBasketGateway) GetBasket(context.Context, ShoppingContext) (Basket, error) {
	return f.basket, f.err
}
func (f *fakeCheckoutBasketGateway) ApplyBasket(context.Context, ShoppingContext, BasketMutation) (BasketMutationResult, error) {
	return BasketMutationResult{}, errors.New("not implemented")
}
func (f *fakeCheckoutBasketGateway) ListTimeSlots(context.Context, ShoppingContext) (TimeSlotList, error) {
	return TimeSlotList{}, errors.New("not implemented")
}
func (f *fakeCheckoutBasketGateway) SelectTimeSlot(context.Context, ShoppingContext, TimeSlotID) (ShoppingContext, error) {
	return ShoppingContext{}, errors.New("not implemented")
}

func boundCheckoutAuth() *stubAuthenticator {
	return &stubAuthenticator{hasIdentity: true, identity: SessionIdentity{AccountID: "account-1"}}
}

func TestCorePrepareOrderFailsClosedWithoutTimeSlot(t *testing.T) {
	core := NewCore(boundCheckoutAuth(), nil, &fakeCheckoutBasketGateway{}, nil, &fakeCheckoutGate{})
	_, err := core.PrepareOrder(t.Context())
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "time_slot_id" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCorePrepareOrderBindsCurrentBasketStoreAndTimeSlot(t *testing.T) {
	basket := Basket{ID: "basket-1", Total: Money{Cents: 999, Currency: "EUR"}}
	var gotBasket Basket
	var gotStore StoreID
	var gotSlot TimeSlotID
	gate := &fakeCheckoutGate{
		prepare: func(_ context.Context, b Basket, s StoreID, t TimeSlotID) (CheckoutApproval, error) {
			gotBasket, gotStore, gotSlot = b, s, t
			return CheckoutApproval{ID: "approval-1", Status: ApprovalPending}, nil
		},
	}
	core := NewCore(boundCheckoutAuth(), nil, &fakeCheckoutBasketGateway{basket: basket}, nil, gate)
	// boundContext() first, so the account binding it applies doesn't wipe
	// the store/basket/timeslot rebound afterward (WithAccount invalidates
	// everything on a change, and is a no-op only once the identity it sees
	// already matches).
	if _, err := core.boundContext(); err != nil {
		t.Fatalf("boundContext() error = %v", err)
	}
	core.rebindContext(core.Context().WithStore("store-1").WithBasket("basket-1").WithTimeSlot("slot-1"))

	got, err := core.PrepareOrder(t.Context())
	if err != nil {
		t.Fatalf("PrepareOrder() error = %v", err)
	}
	if got.ID != "approval-1" || got.Status != ApprovalPending {
		t.Fatalf("PrepareOrder() = %#v", got)
	}
	if gotBasket.ID != "basket-1" || gotStore != "store-1" || gotSlot != "slot-1" {
		t.Fatalf("Prepare() called with basket=%#v store=%q slot=%q", gotBasket, gotStore, gotSlot)
	}
}

func TestCoreOrderStatusPassesFreshBasketToGate(t *testing.T) {
	freshBasket := Basket{ID: "basket-1", Total: Money{Cents: 500, Currency: "EUR"}}
	var gotID ApprovalID
	var gotBasket Basket
	gate := &fakeCheckoutGate{
		status: func(_ context.Context, id ApprovalID, b Basket) (CheckoutApproval, error) {
			gotID, gotBasket = id, b
			return CheckoutApproval{ID: id, Status: ApprovalInvalidated}, nil
		},
	}
	core := NewCore(boundCheckoutAuth(), nil, &fakeCheckoutBasketGateway{basket: freshBasket}, nil, gate)

	got, err := core.OrderStatus(t.Context(), "approval-1")
	if err != nil {
		t.Fatalf("OrderStatus() error = %v", err)
	}
	if got.Status != ApprovalInvalidated {
		t.Fatalf("OrderStatus() = %#v", got)
	}
	if gotID != "approval-1" || gotBasket.ID != "basket-1" {
		t.Fatalf("Status() called with id=%q basket=%#v", gotID, gotBasket)
	}
}

func TestCorePrepareOrderFailsClosedWithoutIdentity(t *testing.T) {
	core := NewCore(&stubAuthenticator{}, nil, &fakeCheckoutBasketGateway{}, nil, &fakeCheckoutGate{})
	_, err := core.PrepareOrder(t.Context())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

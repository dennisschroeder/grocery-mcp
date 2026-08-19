package shopping_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// stubAuthenticator and stubOrdersGateway are narrow test doubles, per the
// phase1-verticals contract: this vertical's tests must not wire a full
// *shopping.Core with all three gateways, since stores/basket don't exist
// in this isolated worktree.
type stubAuthenticator struct {
	identity      shopping.SessionIdentity
	authenticated bool
	refreshCalls  int
	refreshErr    error
}

func (a *stubAuthenticator) Connect() shopping.AuthStatus    { return shopping.AuthStatus{} }
func (a *stubAuthenticator) Status() shopping.AuthStatus     { return shopping.AuthStatus{} }
func (a *stubAuthenticator) Disconnect() shopping.AuthStatus { return shopping.AuthStatus{} }
func (a *stubAuthenticator) Identity() (shopping.SessionIdentity, bool) {
	return a.identity, a.authenticated
}
func (a *stubAuthenticator) RefreshAndValidate(context.Context) error {
	a.refreshCalls++
	return a.refreshErr
}

type stubOrdersGateway struct {
	listOrdersCalls int
	listOrdersErr   error
	orderPage       shopping.OrderPage
	getOrderCalls   int
	getOrderErr     error
	order           shopping.Order
	lastContext     shopping.ShoppingContext
}

func (g *stubOrdersGateway) ListOrders(_ context.Context, sc shopping.ShoppingContext, _ shopping.PageRequest) (shopping.OrderPage, error) {
	g.listOrdersCalls++
	g.lastContext = sc
	return g.orderPage, g.listOrdersErr
}

func (g *stubOrdersGateway) GetOrder(_ context.Context, sc shopping.ShoppingContext, _ shopping.OrderID) (shopping.Order, error) {
	g.getOrderCalls++
	g.lastContext = sc
	return g.order, g.getOrderErr
}

func TestCoreListOrdersBindsContextAndDelegates(t *testing.T) {
	auth := &stubAuthenticator{authenticated: true, identity: shopping.SessionIdentity{AccountID: "account-1", ShopSessionID: "session-1"}}
	want := shopping.OrderPage{Orders: []shopping.OrderSummary{{ID: "order-1"}}, ObservedAt: time.Now()}
	orders := &stubOrdersGateway{orderPage: want}
	core := shopping.NewCore(auth, nil, nil, orders)

	got, err := core.ListOrders(t.Context(), shopping.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Orders) != 1 || got.Orders[0].ID != "order-1" {
		t.Fatalf("ListOrders() = %#v", got)
	}
	if orders.listOrdersCalls != 1 {
		t.Fatalf("gateway called %d times, want 1", orders.listOrdersCalls)
	}
	if orders.lastContext.AccountID != "account-1" {
		t.Fatalf("bound context = %#v, want account-1", orders.lastContext)
	}
}

func TestCoreListOrdersFailsClosedWithoutIdentity(t *testing.T) {
	auth := &stubAuthenticator{authenticated: false}
	orders := &stubOrdersGateway{}
	core := shopping.NewCore(auth, nil, nil, orders)

	_, err := core.ListOrders(t.Context(), shopping.PageRequest{})
	var authErr *shopping.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want *shopping.AuthError", err)
	}
	if orders.listOrdersCalls != 0 {
		t.Fatalf("gateway called %d times, want 0", orders.listOrdersCalls)
	}
}

func TestCoreGetOrderDelegates(t *testing.T) {
	auth := &stubAuthenticator{authenticated: true, identity: shopping.SessionIdentity{AccountID: "account-1"}}
	orders := &stubOrdersGateway{order: shopping.Order{OrderSummary: shopping.OrderSummary{ID: "order-9"}}}
	core := shopping.NewCore(auth, nil, nil, orders)

	got, err := core.GetOrder(t.Context(), "order-9")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "order-9" {
		t.Fatalf("GetOrder() = %#v", got)
	}
	if orders.getOrderCalls != 1 {
		t.Fatalf("gateway called %d times, want 1", orders.getOrderCalls)
	}
}

func TestCoreListOrdersRetriesOnceAfterAuthErrorAndRefresh(t *testing.T) {
	auth := &stubAuthenticator{authenticated: true, identity: shopping.SessionIdentity{AccountID: "account-1"}}
	orders := &stubOrdersGateway{listOrdersErr: &shopping.AuthError{Operation: "orders.list"}}
	core := shopping.NewCore(auth, nil, nil, orders)

	_, err := core.ListOrders(t.Context(), shopping.PageRequest{})
	var authErr *shopping.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want *shopping.AuthError after exhausted retry", err)
	}
	if orders.listOrdersCalls != 2 {
		t.Fatalf("gateway called %d times, want 2 (one retry)", orders.listOrdersCalls)
	}
	if auth.refreshCalls != 1 {
		t.Fatalf("refresh called %d times, want 1", auth.refreshCalls)
	}
}

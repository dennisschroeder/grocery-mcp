package shopping

import (
	"context"
	"errors"
	"testing"
)

// stubStoresGateway is a narrow test double for StoresGateway only — this
// vertical's isolated worktree has no basket/orders gateway implementation
// to wire a full ReweGateway with, per the frozen contract.
type stubStoresGateway struct {
	searchStoresFn   func(context.Context, ShoppingContext, StoreSearch) (StorePage, error)
	selectStoreFn    func(context.Context, ShoppingContext, StoreID) (ShoppingContext, error)
	searchProductsFn func(context.Context, ShoppingContext, ProductSearch) (ProductPage, error)
}

func (g stubStoresGateway) SearchStores(ctx context.Context, current ShoppingContext, search StoreSearch) (StorePage, error) {
	return g.searchStoresFn(ctx, current, search)
}

func (g stubStoresGateway) SelectStore(ctx context.Context, current ShoppingContext, id StoreID) (ShoppingContext, error) {
	return g.selectStoreFn(ctx, current, id)
}

func (g stubStoresGateway) SearchProducts(ctx context.Context, current ShoppingContext, search ProductSearch) (ProductPage, error) {
	return g.searchProductsFn(ctx, current, search)
}

// stubAuthenticator is shared across this package's tests — see
// authenticator_stub_test.go.

func TestCoreStoresSearchBindsContextAndCallsGateway(t *testing.T) {
	auth := &stubAuthenticator{identity: SessionIdentity{AccountID: "account-1"}, hasIdentity: true}
	var gotContext ShoppingContext
	gateway := stubStoresGateway{
		searchStoresFn: func(_ context.Context, current ShoppingContext, _ StoreSearch) (StorePage, error) {
			gotContext = current
			return StorePage{Stores: []Store{{ID: "123456"}}}, nil
		},
	}
	core := NewCore(auth, gateway, nil, nil)
	page, err := core.StoresSearch(t.Context(), StoreSearch{PostalCode: "10115"})
	if err != nil {
		t.Fatalf("StoresSearch() error = %v", err)
	}
	if len(page.Stores) != 1 {
		t.Fatalf("unexpected page: %#v", page)
	}
	if gotContext.AccountID != "account-1" {
		t.Fatalf("gateway did not receive a bound context: %#v", gotContext)
	}
}

func TestCoreStoresSearchFailsClosedWhenUnauthenticated(t *testing.T) {
	auth := &stubAuthenticator{}
	core := NewCore(auth, stubStoresGateway{}, nil, nil)
	_, err := core.StoresSearch(t.Context(), StoreSearch{PostalCode: "10115"})
	var target *AuthError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCoreStoresSearchRetriesOnceAfterSuccessfulRefresh(t *testing.T) {
	auth := &stubAuthenticator{identity: SessionIdentity{AccountID: "account-1"}, hasIdentity: true}
	attempts := 0
	gateway := stubStoresGateway{
		searchStoresFn: func(context.Context, ShoppingContext, StoreSearch) (StorePage, error) {
			attempts++
			if attempts == 1 {
				return StorePage{}, &AuthError{Operation: "stores.search"}
			}
			return StorePage{Stores: []Store{{ID: "123456"}}}, nil
		},
	}
	core := NewCore(auth, gateway, nil, nil)
	page, err := core.StoresSearch(t.Context(), StoreSearch{PostalCode: "10115"})
	if err != nil {
		t.Fatalf("StoresSearch() error = %v", err)
	}
	if len(page.Stores) != 1 || attempts != 2 || auth.refreshCalls != 1 {
		t.Fatalf("unexpected retry behavior: page=%#v attempts=%d refreshCalls=%d", page, attempts, auth.refreshCalls)
	}
}

func TestCoreProductsSearchBindsContextAndCallsGateway(t *testing.T) {
	auth := &stubAuthenticator{identity: SessionIdentity{AccountID: "account-1"}, hasIdentity: true}
	gateway := stubStoresGateway{
		searchProductsFn: func(_ context.Context, current ShoppingContext, search ProductSearch) (ProductPage, error) {
			if search.Query != "milch" {
				t.Fatalf("unexpected search: %#v", search)
			}
			return ProductPage{Products: []Product{{ID: "product-1"}}}, nil
		},
	}
	core := NewCore(auth, gateway, nil, nil)
	page, err := core.ProductsSearch(t.Context(), ProductSearch{Query: "milch"})
	if err != nil {
		t.Fatalf("ProductsSearch() error = %v", err)
	}
	if len(page.Products) != 1 {
		t.Fatalf("unexpected page: %#v", page)
	}
}

func TestCoreProductsSearchFailsClosedWhenUnauthenticated(t *testing.T) {
	auth := &stubAuthenticator{}
	core := NewCore(auth, stubStoresGateway{}, nil, nil)
	_, err := core.ProductsSearch(t.Context(), ProductSearch{Query: "milch"})
	var target *AuthError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCoreSelectStoreRebindsContextOnSuccess(t *testing.T) {
	auth := &stubAuthenticator{identity: SessionIdentity{AccountID: "account-1"}, hasIdentity: true}
	gateway := stubStoresGateway{
		selectStoreFn: func(_ context.Context, current ShoppingContext, id StoreID) (ShoppingContext, error) {
			return current.WithStore(id), nil
		},
	}
	core := NewCore(auth, gateway, nil, nil)
	next, err := core.SelectStore(t.Context(), StoreID("123456"), "10115")
	if err != nil {
		t.Fatalf("SelectStore() error = %v", err)
	}
	if next.StoreID != "123456" || next.PostalCode != "10115" {
		t.Fatalf("unexpected context: %#v", next)
	}
	if core.Context().StoreID != "123456" || core.Context().PostalCode != "10115" {
		t.Fatalf("Core did not rebind: %#v", core.Context())
	}
}

func TestCoreSelectStoreDoesNotRebindOnFailure(t *testing.T) {
	auth := &stubAuthenticator{identity: SessionIdentity{AccountID: "account-1"}, hasIdentity: true}
	gateway := stubStoresGateway{
		selectStoreFn: func(context.Context, ShoppingContext, StoreID) (ShoppingContext, error) {
			return ShoppingContext{}, &ValidationError{Operation: "stores.select", Field: "store_id", Problem: ValidationInvalid}
		},
	}
	core := NewCore(auth, gateway, nil, nil)
	_, err := core.SelectStore(t.Context(), StoreID("bad"), "10115")
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
	if core.Context().StoreID != "" {
		t.Fatalf("Core rebound on a failed selection: %#v", core.Context())
	}
}

func TestCoreSelectStoreDoesNotRetryAMutation(t *testing.T) {
	auth := &stubAuthenticator{identity: SessionIdentity{AccountID: "account-1"}, hasIdentity: true}
	attempts := 0
	gateway := stubStoresGateway{
		selectStoreFn: func(context.Context, ShoppingContext, StoreID) (ShoppingContext, error) {
			attempts++
			return ShoppingContext{}, &AuthError{Operation: "stores.select"}
		},
	}
	core := NewCore(auth, gateway, nil, nil)
	_, err := core.SelectStore(t.Context(), StoreID("123456"), "10115")
	var target *AuthError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("SelectStore retried a mutation: attempts=%d, want 1", attempts)
	}
	if auth.refreshCalls != 0 {
		t.Fatalf("SelectStore triggered a refresh: refreshCalls=%d, want 0", auth.refreshCalls)
	}
}

func TestCoreSelectStoreFailsClosedWhenUnauthenticated(t *testing.T) {
	auth := &stubAuthenticator{}
	core := NewCore(auth, stubStoresGateway{}, nil, nil)
	_, err := core.SelectStore(t.Context(), StoreID("123456"), "10115")
	var target *AuthError
	if !errors.As(err, &target) {
		t.Fatalf("unexpected error: %v", err)
	}
}

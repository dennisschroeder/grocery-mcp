package shopping

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

type memorySharedState struct {
	mu     sync.Mutex
	lockMu sync.Mutex
	values map[string]json.RawMessage
}

func (s *memorySharedState) LockState(_ context.Context, _ string) (func(), error) {
	s.lockMu.Lock()
	return s.lockMu.Unlock, nil
}

func (s *memorySharedState) LoadState(_ context.Context, key string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.values[key]
	return append(json.RawMessage(nil), value...), found, nil
}

func (s *memorySharedState) StoreState(_ context.Context, key string, value json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = append(json.RawMessage(nil), value...)
	return nil
}

func TestCoresShareAccountScopedShoppingContextWithoutSharingSession(t *testing.T) {
	state := &memorySharedState{values: make(map[string]json.RawMessage)}
	contexts := NewSharedContextStore(state)
	firstGateway := stubStoresGateway{
		selectStoreFn: func(_ context.Context, current ShoppingContext, id StoreID) (ShoppingContext, error) {
			return current.WithStore(id), nil
		},
	}
	first := NewCore(&stubAuthenticator{
		hasIdentity: true,
		identity:    SessionIdentity{AccountID: "account-1", ShopSessionID: "session-code"},
	}, firstGateway, nil, nil, nil, contexts)
	if _, err := first.SelectStore(t.Context(), "660500", "10115"); err != nil {
		t.Fatal(err)
	}

	var received ShoppingContext
	secondGateway := stubStoresGateway{
		searchProductsFn: func(_ context.Context, current ShoppingContext, _ ProductSearch) (ProductPage, error) {
			received = current
			return ProductPage{}, nil
		},
	}
	second := NewCore(&stubAuthenticator{
		hasIdentity: true,
		identity:    SessionIdentity{AccountID: "account-1", ShopSessionID: "session-cowork"},
	}, secondGateway, nil, nil, nil, contexts)
	if _, err := second.ProductsSearch(t.Context(), ProductSearch{Query: "milk"}); err != nil {
		t.Fatal(err)
	}
	if received.StoreID != "660500" || received.PostalCode != "10115" {
		t.Fatalf("shared context was not loaded: %#v", received)
	}
	if received.ShopSessionID != "session-cowork" {
		t.Fatalf("another process's session leaked into the context: %#v", received)
	}
	key := sharedContextKey("account-1")
	if string(state.values[key]) == "" {
		t.Fatal("account-scoped context was not stored")
	}
	var stored map[string]any
	if err := json.Unmarshal(state.values[key], &stored); err != nil {
		t.Fatal(err)
	}
	if _, leaked := stored["shop_session_id"]; leaked {
		t.Fatalf("session was persisted: %s", state.values[key])
	}
}

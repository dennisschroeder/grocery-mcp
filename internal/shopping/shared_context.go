package shopping

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
)

type SharedState interface {
	LoadState(context.Context, string) (json.RawMessage, bool, error)
	StoreState(context.Context, string, json.RawMessage) error
	LockState(context.Context, string) (func(), error)
}

type SharedContextStore struct {
	state SharedState
}

func NewSharedContextStore(state SharedState) *SharedContextStore {
	return &SharedContextStore{state: state}
}

type storedShoppingContext struct {
	StoreID    StoreID    `json:"store_id,omitempty"`
	PostalCode string     `json:"postal_code,omitempty"`
	BasketID   BasketID   `json:"basket_id,omitempty"`
	TimeSlotID TimeSlotID `json:"timeslot_id,omitempty"`
}

func (s *SharedContextStore) Load(ctx context.Context, accountID AccountID) (ShoppingContext, bool, error) {
	value, found, err := s.state.LoadState(ctx, sharedContextKey(accountID))
	if err != nil {
		return ShoppingContext{}, false, &BridgeUnavailableError{Operation: "load shopping context"}
	}
	if !found {
		return ShoppingContext{}, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var stored storedShoppingContext
	if err := decoder.Decode(&stored); err != nil {
		return ShoppingContext{}, false, &BridgeUnavailableError{Operation: "load shopping context"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ShoppingContext{}, false, &BridgeUnavailableError{Operation: "load shopping context"}
	}
	return ShoppingContext{
		AccountID:  accountID,
		StoreID:    stored.StoreID,
		PostalCode: stored.PostalCode,
		BasketID:   stored.BasketID,
		TimeSlotID: stored.TimeSlotID,
	}, true, nil
}

func (s *SharedContextStore) Store(ctx context.Context, current ShoppingContext) error {
	if current.AccountID == "" {
		return &BridgeUnavailableError{Operation: "store shopping context"}
	}
	value, err := json.Marshal(storedShoppingContext{
		StoreID:    current.StoreID,
		PostalCode: current.PostalCode,
		BasketID:   current.BasketID,
		TimeSlotID: current.TimeSlotID,
	})
	if err != nil {
		return &BridgeUnavailableError{Operation: "store shopping context"}
	}
	if err := s.state.StoreState(ctx, sharedContextKey(current.AccountID), value); err != nil {
		return &BridgeUnavailableError{Operation: "store shopping context"}
	}
	return nil
}

func (s *SharedContextStore) Lock(ctx context.Context, accountID AccountID) (func(), error) {
	unlock, err := s.state.LockState(ctx, sharedContextKey(accountID))
	if err != nil {
		return nil, &BridgeUnavailableError{Operation: "lock shopping context"}
	}
	return unlock, nil
}

func sharedContextKey(accountID AccountID) string {
	return fmt.Sprintf("shopping:%x", sha256.Sum256([]byte(accountID)))
}

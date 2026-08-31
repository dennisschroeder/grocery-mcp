package shopping

import (
	"context"
	"sync"
)

// Authenticator embeds SessionRefresher so Core can drive the read-retry
// policy directly — *auth.Service (via cmd/grocery-mcp's live wiring
// adapter) already satisfies every method here.
type Authenticator interface {
	Connect() AuthStatus
	Status() AuthStatus
	Disconnect() AuthStatus
	Identity() (SessionIdentity, bool)
	SessionRefresher
}

type ContextStore interface {
	Load(context.Context, AccountID) (ShoppingContext, bool, error)
	Store(context.Context, ShoppingContext) error
	Lock(context.Context, AccountID) (func(), error)
}

// Core is the external interface and test surface between MCP tools and
// REWE. Each Phase 1 vertical gets its own narrow gateway field —
// StoresGateway, BasketGateway, OrdersGateway — rather than one field typed
// as the full ReweGateway, so each vertical's slice type-checks and tests
// independently of whether the other two are implemented yet.
type Core struct {
	auth Authenticator

	mu       sync.RWMutex
	context  ShoppingContext
	contexts ContextStore

	stores   StoresGateway
	basket   BasketGateway
	orders   OrdersGateway
	checkout CheckoutGate
}

func NewCore(auth Authenticator, stores StoresGateway, basket BasketGateway, orders OrdersGateway, checkout CheckoutGate, contexts ...ContextStore) *Core {
	core := &Core{auth: auth, stores: stores, basket: basket, orders: orders, checkout: checkout}
	if len(contexts) > 0 {
		core.contexts = contexts[0]
	}
	return core
}

func (c *Core) AuthConnect() AuthStatus {
	return c.auth.Connect()
}

func (c *Core) AuthStatus() AuthStatus {
	return c.auth.Status()
}

func (c *Core) AuthDisconnect() AuthStatus {
	return c.auth.Disconnect()
}

// boundContext refreshes the Shopping Context's account/session binding from
// the live auth identity before every gateway call. WithAccount/WithSession
// are no-ops when unchanged, so a stable identity never wipes a valid store,
// basket, or timeslot selection — but a different or absent identity fails
// closed (AuthError) or wipes everything downstream, per card #6's contract.
func (c *Core) boundContext(ctx context.Context) (ShoppingContext, error) {
	identity, ok := c.auth.Identity()
	if !ok {
		return ShoppingContext{}, &AuthError{Operation: "shopping context"}
	}
	return c.boundContextFor(ctx, identity)
}

func (c *Core) boundContextFor(ctx context.Context, identity SessionIdentity) (ShoppingContext, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.contexts != nil {
		shared, found, err := c.contexts.Load(ctx, identity.AccountID)
		if err != nil {
			return ShoppingContext{}, err
		}
		if found {
			c.context = shared
		}
	}
	c.context = c.context.WithAccount(identity.AccountID).WithSession(identity.ShopSessionID)
	return c.context, nil
}

// rebindContext installs a new Shopping Context after a successful
// selection (e.g. SelectStore, SelectTimeSlot). Call through a vertical's
// typed Select method, not directly — this only stores what the gateway
// already confirmed.
func (c *Core) rebindContext(ctx context.Context, next ShoppingContext) error {
	c.mu.Lock()
	c.context = next
	c.mu.Unlock()
	if c.contexts != nil {
		if err := c.contexts.Store(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

func (c *Core) lockContext(ctx context.Context) (SessionIdentity, func(), error) {
	identity, ok := c.auth.Identity()
	if !ok {
		return SessionIdentity{}, nil, &AuthError{Operation: "shopping context"}
	}
	if c.contexts == nil {
		return identity, func() {}, nil
	}
	unlock, err := c.contexts.Lock(ctx, identity.AccountID)
	if err != nil {
		return SessionIdentity{}, nil, err
	}
	current, ok := c.auth.Identity()
	if !ok || current.AccountID != identity.AccountID || current.ShopSessionID != identity.ShopSessionID {
		unlock()
		return SessionIdentity{}, nil, &AuthError{Operation: "shopping context"}
	}
	return identity, unlock, nil
}

// Context returns the current Shopping Context for inspection (e.g. by a
// vertical's own tool handlers that need the selected store/basket without
// making a gateway call).
func (c *Core) Context() ShoppingContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.context
}

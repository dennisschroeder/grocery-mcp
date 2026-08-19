package shopping

import "sync"

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

// Core is the external interface and test surface between MCP tools and
// REWE. Each Phase 1 vertical gets its own narrow gateway field —
// StoresGateway, BasketGateway, OrdersGateway — rather than one field typed
// as the full ReweGateway, so each vertical's slice type-checks and tests
// independently of whether the other two are implemented yet.
type Core struct {
	auth Authenticator

	mu      sync.RWMutex
	context ShoppingContext

	stores StoresGateway
	basket BasketGateway
	orders OrdersGateway
}

func NewCore(auth Authenticator, stores StoresGateway, basket BasketGateway, orders OrdersGateway) *Core {
	return &Core{auth: auth, stores: stores, basket: basket, orders: orders}
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
func (c *Core) boundContext() (ShoppingContext, error) {
	identity, ok := c.auth.Identity()
	if !ok {
		return ShoppingContext{}, &AuthError{Operation: "shopping context"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.context = c.context.WithAccount(identity.AccountID).WithSession(identity.ShopSessionID)
	return c.context, nil
}

// rebindContext installs a new Shopping Context after a successful
// selection (e.g. SelectStore, SelectTimeSlot). Call through a vertical's
// typed Select method, not directly — this only stores what the gateway
// already confirmed.
func (c *Core) rebindContext(next ShoppingContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.context = next
}

// Context returns the current Shopping Context for inspection (e.g. by a
// vertical's own tool handlers that need the selected store/basket without
// making a gateway call).
func (c *Core) Context() ShoppingContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.context
}

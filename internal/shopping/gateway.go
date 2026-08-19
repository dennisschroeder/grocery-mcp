package shopping

import "context"

// ReweGateway is the only seam through which ShoppingCore reaches REWE.
// Implementations own transport, decoding, session refresh, and retry
// policy. Composed from three narrower per-vertical interfaces so each
// Phase 1 vertical slice can be built and type-checked independently before
// all three land in one implementation (internal/rewe.Gateway).
type ReweGateway interface {
	SessionValidator
	StoresGateway
	BasketGateway
	OrdersGateway
}

// SessionValidator is part of the stable ReweGateway contract from card #5
// and is intentionally distinct from auth.Validator, which drives the
// browser-executed session_identity proof auth.Service actually uses.
// Nothing implements this yet — session liveness is proven independently
// through the auth package (see ADR-0002) — it's kept for the reflection-
// based type-stability test in gateway_test.go and as a placeholder should
// a gateway-level identity check turn out to be needed later.
type SessionValidator interface {
	ValidateSession(context.Context, ShoppingContext) (SessionIdentity, error)
}

type StoresGateway interface {
	SearchStores(context.Context, ShoppingContext, StoreSearch) (StorePage, error)
	SelectStore(context.Context, ShoppingContext, StoreID) (ShoppingContext, error)
	SearchProducts(context.Context, ShoppingContext, ProductSearch) (ProductPage, error)
}

type BasketGateway interface {
	GetBasket(context.Context, ShoppingContext) (Basket, error)
	ApplyBasket(context.Context, ShoppingContext, BasketMutation) (BasketMutationResult, error)
	ListTimeSlots(context.Context, ShoppingContext) (TimeSlotList, error)
	SelectTimeSlot(context.Context, ShoppingContext, TimeSlotID) (ShoppingContext, error)
}

type OrdersGateway interface {
	ListOrders(context.Context, ShoppingContext, PageRequest) (OrderPage, error)
	GetOrder(context.Context, ShoppingContext, OrderID) (Order, error)
}

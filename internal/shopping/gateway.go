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
	GetBasketListings(context.Context, ShoppingContext) (BasketListingSnapshot, error)
	ApplyBasket(context.Context, ShoppingContext, BasketMutation) (BasketMutationResult, error)
	ListTimeSlots(context.Context, ShoppingContext) (TimeSlotList, error)
	SelectTimeSlot(context.Context, ShoppingContext, TimeSlotID) (ShoppingContext, error)
}

type OrdersGateway interface {
	ListOrders(context.Context, ShoppingContext, PageRequest) (OrderPage, error)
	GetOrder(context.Context, ShoppingContext, OrderID) (Order, error)
}

// CheckoutGate is deliberately not part of ReweGateway — DESIGN.md draws it
// as ShoppingCore's other direct dependency, sibling to ReweGateway, not a
// REWE-facing vertical: it owns human approval, commit, and reconciliation,
// never raw REWE transport/decoding. Prepare binds one Basket/StoreID/
// TimeSlotID snapshot to a short-lived approval and returns it immediately;
// it must never block on the human's decision. Status is the only way to
// observe an approval's outcome — including whether Prepare's own snapshot
// has since been invalidated by a relevant state change.
type CheckoutGate interface {
	Prepare(ctx context.Context, current Basket, storeID StoreID, timeSlotID TimeSlotID) (CheckoutApproval, error)
	Status(ctx context.Context, id ApprovalID, current Basket) (CheckoutApproval, error)
}

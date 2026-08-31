package shopping

import "time"

type AuthState string

const (
	AuthUnauthenticated AuthState = "Unauthenticated"
	AuthBootstrapping   AuthState = "Bootstrapping"
	AuthValidating      AuthState = "Validating"
	AuthActive          AuthState = "Active"
	AuthRefreshing      AuthState = "Refreshing"
	AuthReauthRequired  AuthState = "ReauthRequired"
	AuthFailed          AuthState = "Failed"
)

type AuthStatus struct {
	State           AuthState `json:"state"`
	ActionRequired  bool      `json:"action_required"`
	SessionRevision uint64    `json:"session_revision"`
	ObservedAt      time.Time `json:"observed_at"`
}

type (
	AccountID     string
	ShopSessionID string
	StoreID       string
	ProductID     string
	BasketID      string
	TimeSlotID    string
	OrderID       string
)

type ShoppingContext struct {
	AccountID     AccountID
	ShopSessionID ShopSessionID
	StoreID       StoreID
	PostalCode    string
	BasketID      BasketID
	TimeSlotID    TimeSlotID
}

type SessionIdentity struct {
	AccountID     AccountID
	ShopSessionID ShopSessionID
	ObservedAt    time.Time
}

type Money struct {
	Cents    int64
	Currency string
}

type StoreSearch struct {
	PostalCode string
	Query      string
	Limit      int
}

type Store struct {
	ID             StoreID
	Name           string
	Address        StoreAddress
	Pickup         bool
	DistanceMeters float64
	ObservedAt     time.Time
}

type StoreAddress struct {
	Street     string
	PostalCode string
	City       string
}

type StorePage struct {
	Stores     []Store
	HasMore    bool
	ObservedAt time.Time
}

type ProductSearch struct {
	Query  string
	Limit  int
	Offset int
}

type Product struct {
	ID         ProductID
	Name       string
	Brand      string
	Unit       string
	Price      Money
	Available  bool
	ObservedAt time.Time
}

type ProductPage struct {
	Products   []Product
	HasMore    bool
	ObservedAt time.Time
}

type BasketItem struct {
	ProductID ProductID
	Name      string
	Quantity  int
	UnitPrice Money
	LineTotal Money
}

type Basket struct {
	ID         BasketID
	StoreID    StoreID
	Items      []BasketItem
	Subtotal   Money
	Fees       Money
	Total      Money
	TimeSlotID TimeSlotID
	ObservedAt time.Time
}

type BasketListingSnapshot struct {
	ListingIDs []ProductID
	ObservedAt time.Time
}

type BasketChange struct {
	ProductID ProductID
	Quantity  int
}

type BasketMutation struct {
	Changes []BasketChange
}

type BasketChangeStatus string

const (
	BasketChangeApplied  BasketChangeStatus = "applied"
	BasketChangeAdjusted BasketChangeStatus = "adjusted"
	BasketChangeRejected BasketChangeStatus = "rejected"
	BasketChangeUnknown  BasketChangeStatus = "unknown"
)

type BasketChangeProblem string

const (
	BasketProblemUnavailable   BasketChangeProblem = "unavailable"
	BasketProblemOutOfStock    BasketChangeProblem = "out_of_stock"
	BasketProblemLimitExceeded BasketChangeProblem = "limit_exceeded"
	BasketProblemAuthInvalid   BasketChangeProblem = "auth_invalid"
	BasketProblemRateLimited   BasketChangeProblem = "rate_limited"
	BasketProblemInvalid       BasketChangeProblem = "invalid"
	BasketProblemUpstream      BasketChangeProblem = "upstream_changed"
	BasketProblemUnknown       BasketChangeProblem = "unknown"
)

type BasketChangeOutcome struct {
	ProductID         ProductID
	RequestedQuantity int
	AppliedQuantity   int
	Status            BasketChangeStatus
	Problem           BasketChangeProblem
}

type BasketMutationResult struct {
	Basket                Basket
	Outcomes              []BasketChangeOutcome
	Reconciled            bool
	ReconciliationProblem ReconciliationProblem
	ContextSynchronized   bool
}

type ReconciliationProblem string

const (
	ReconciliationNone                   ReconciliationProblem = ""
	ReconciliationAuthInvalid            ReconciliationProblem = "auth_invalid"
	ReconciliationRateLimited            ReconciliationProblem = "rate_limited"
	ReconciliationUpstreamChanged        ReconciliationProblem = "upstream_changed"
	ReconciliationBasketUnavailable      ReconciliationProblem = "basket_unavailable"
	ReconciliationBasketIDUnknown        ReconciliationProblem = "basket_id_unknown"
	ReconciliationIncompatibleItemResult ReconciliationProblem = "incompatible_item_result"
	ReconciliationBridgeInterrupted      ReconciliationProblem = "bridge_interrupted"
)

type TimeSlotSelectionResult struct {
	Context             ShoppingContext
	Reconciled          bool
	ContextSynchronized bool
}

type TimeSlot struct {
	ID         TimeSlotID
	StoreID    StoreID
	StartsAt   time.Time
	EndsAt     time.Time
	Fee        Money
	Available  bool
	Selected   bool
	ObservedAt time.Time
}

type TimeSlotList struct {
	TimeSlots  []TimeSlot
	ObservedAt time.Time
}

type PageRequest struct {
	Limit  int
	Offset int
}

type OrderStatus string

const (
	OrderStatusOpen      OrderStatus = "open"
	OrderStatusReady     OrderStatus = "ready"
	OrderStatusCollected OrderStatus = "collected"
	OrderStatusCancelled OrderStatus = "cancelled"
	OrderStatusUnknown   OrderStatus = "unknown"
)

type OrderSummary struct {
	ID         OrderID
	Status     OrderStatus
	StoreID    StoreID
	PickupAt   time.Time
	Total      Money
	ObservedAt time.Time
}

type Order struct {
	OrderSummary
	Items []BasketItem
}

type OrderPage struct {
	Orders     []OrderSummary
	HasMore    bool
	ObservedAt time.Time
}

type ApprovalID string

type ApprovalStatus string

const (
	ApprovalPending      ApprovalStatus = "pending"
	ApprovalApproved     ApprovalStatus = "approved"
	ApprovalDeclined     ApprovalStatus = "declined"
	ApprovalExpired      ApprovalStatus = "expired"
	ApprovalInvalidated  ApprovalStatus = "invalidated"
	ApprovalCommitted    ApprovalStatus = "committed"
	ApprovalCommitFailed ApprovalStatus = "commit_failed"
)

// CheckoutApproval binds one Basket/StoreID/TimeSlotID snapshot to a
// short-lived, human-approvable order. AGENTS.md: "Every Phase 2 order
// requires a fresh, out-of-band human approval bound to the exact basket,
// price, store, and timeslot" and "changing one invalidates dependent
// state." The full Basket (not just its ID) is the snapshot: the same
// BasketID can still hold different contents than when the approval was
// created, and that must invalidate it too.
type CheckoutApproval struct {
	ID            ApprovalID
	Status        ApprovalStatus
	Basket        Basket
	StoreID       StoreID
	TimeSlotID    TimeSlotID
	ApprovalURL   string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	OrderID       OrderID // set only once Status is ApprovalCommitted
	FailureReason string  // set only once Status is ApprovalCommitFailed
}

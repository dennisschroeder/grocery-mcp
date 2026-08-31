package shopping

import "context"

// PrepareOrder reloads the authoritative basket and binds it, the selected
// store, and the selected timeslot into a short-lived CheckoutGate approval.
// A read (reloading the basket), but never wrapped in ReadWithRefresh: it
// mints an approval as a side effect, and AGENTS.md's single-retry-after-
// refresh policy is for idempotent reads only.
func (c *Core) PrepareOrder(ctx context.Context) (CheckoutApproval, error) {
	identity, unlock, err := c.lockContext(ctx)
	if err != nil {
		return CheckoutApproval{}, err
	}
	defer unlock()
	shoppingContext, err := c.boundContextFor(ctx, identity)
	if err != nil {
		return CheckoutApproval{}, err
	}
	if shoppingContext.TimeSlotID == "" {
		return CheckoutApproval{}, &ValidationError{Operation: "prepare order", Field: "time_slot_id", Problem: ValidationMissing}
	}
	basket, err := c.basket.GetBasket(ctx, shoppingContext)
	if err != nil {
		return CheckoutApproval{}, err
	}
	basket.StoreID = shoppingContext.StoreID
	basket.TimeSlotID = shoppingContext.TimeSlotID
	return c.checkout.Prepare(ctx, basket, shoppingContext.StoreID, shoppingContext.TimeSlotID)
}

// OrderStatus reports one approval's current status, re-checking it against
// the authoritative basket on every call — CheckoutGate.Status, not this
// method, owns deciding whether that means the approval is still valid.
func (c *Core) OrderStatus(ctx context.Context, id ApprovalID) (CheckoutApproval, error) {
	identity, unlock, err := c.lockContext(ctx)
	if err != nil {
		return CheckoutApproval{}, err
	}
	defer unlock()
	shoppingContext, err := c.boundContextFor(ctx, identity)
	if err != nil {
		return CheckoutApproval{}, err
	}
	basket, err := c.basket.GetBasket(ctx, shoppingContext)
	if err != nil {
		return CheckoutApproval{}, err
	}
	basket.StoreID = shoppingContext.StoreID
	basket.TimeSlotID = shoppingContext.TimeSlotID
	return c.checkout.Status(ctx, id, basket)
}

package shopping

import "context"

// GetBasket is a read: it may retry once after a successful session
// refresh, per ReadWithRefresh's policy.
func (c *Core) GetBasket(ctx context.Context) (Basket, error) {
	shoppingContext, err := c.boundContext()
	if err != nil {
		return Basket{}, err
	}
	return ReadWithRefresh(ctx, c.auth, func(ctx context.Context) (Basket, error) {
		return c.basket.GetBasket(ctx, shoppingContext)
	})
}

// ApplyBasket is a mutation: never wrapped in ReadWithRefresh. A partial
// per-item failure comes back inside BasketMutationResult.Outcomes, not as
// an error for the whole call — see BasketGateway.ApplyBasket. On success it
// rebinds Core's ShoppingContext to the basket ID REWE assigned — the first
// successful add is the only place that ID is ever discovered, and without
// this, every later GetBasket/ApplyBasket call would still see an empty
// BasketID and fail closed with ValidationMissing.
func (c *Core) ApplyBasket(ctx context.Context, mutation BasketMutation) (BasketMutationResult, error) {
	shoppingContext, err := c.boundContext()
	if err != nil {
		return BasketMutationResult{}, err
	}
	result, err := c.basket.ApplyBasket(ctx, shoppingContext, mutation)
	if err != nil {
		return BasketMutationResult{}, err
	}
	if result.Basket.ID != "" {
		c.rebindContext(shoppingContext.WithBasket(result.Basket.ID))
	}
	return result, nil
}

// ListTimeSlots is a read: it may retry once after a successful session
// refresh, per ReadWithRefresh's policy.
func (c *Core) ListTimeSlots(ctx context.Context) (TimeSlotList, error) {
	shoppingContext, err := c.boundContext()
	if err != nil {
		return TimeSlotList{}, err
	}
	return ReadWithRefresh(ctx, c.auth, func(ctx context.Context) (TimeSlotList, error) {
		return c.basket.ListTimeSlots(ctx, shoppingContext)
	})
}

// SelectTimeSlot is a mutation: never wrapped in ReadWithRefresh. On
// success it rebinds Core's ShoppingContext to what the gateway confirmed.
func (c *Core) SelectTimeSlot(ctx context.Context, slot TimeSlotID) (ShoppingContext, error) {
	shoppingContext, err := c.boundContext()
	if err != nil {
		return ShoppingContext{}, err
	}
	next, err := c.basket.SelectTimeSlot(ctx, shoppingContext, slot)
	if err != nil {
		return ShoppingContext{}, err
	}
	c.rebindContext(next)
	return next, nil
}

package shopping

import "context"

// StoresSearch and ProductsSearch are reads: wrapped in ReadWithRefresh so a
// lapsed session gets exactly one retry after a successful refresh, per
// AGENTS.md. boundContext runs inside the retried closure itself, not
// before it, so a retry picks up whatever identity the refresh produced.
func (c *Core) StoresSearch(ctx context.Context, search StoreSearch) (StorePage, error) {
	return ReadWithRefresh(ctx, c.auth, func(ctx context.Context) (StorePage, error) {
		bound, err := c.boundContext()
		if err != nil {
			return StorePage{}, err
		}
		return c.stores.SearchStores(ctx, bound, search)
	})
}

func (c *Core) ProductsSearch(ctx context.Context, search ProductSearch) (ProductPage, error) {
	return ReadWithRefresh(ctx, c.auth, func(ctx context.Context) (ProductPage, error) {
		bound, err := c.boundContext()
		if err != nil {
			return ProductPage{}, err
		}
		return c.stores.SearchProducts(ctx, bound, search)
	})
}

// SelectStore is a selection, not a read: no ReadWithRefresh retry, per
// AGENTS.md ("mutations require proven idempotency or reconciliation").
// rebindContext only runs after the gateway confirms the ID, never before.
// postalCode is attached to the resulting context (not validated by the
// gateway, which only owns the market ID) because REWE's timeslot endpoint
// requires it alongside the market ID as a request header.
func (c *Core) SelectStore(ctx context.Context, id StoreID, postalCode string) (ShoppingContext, error) {
	bound, err := c.boundContext()
	if err != nil {
		return ShoppingContext{}, err
	}
	next, err := c.stores.SelectStore(ctx, bound, id)
	if err != nil {
		return ShoppingContext{}, err
	}
	next = next.WithPostalCode(postalCode)
	c.rebindContext(next)
	return next, nil
}

package shopping

import "context"

func (c *Core) ListOrders(ctx context.Context, page PageRequest) (OrderPage, error) {
	return ReadWithRefresh(ctx, c.auth, func(ctx context.Context) (OrderPage, error) {
		sc, err := c.boundContext()
		if err != nil {
			return OrderPage{}, err
		}
		return c.orders.ListOrders(ctx, sc, page)
	})
}

func (c *Core) GetOrder(ctx context.Context, id OrderID) (Order, error) {
	return ReadWithRefresh(ctx, c.auth, func(ctx context.Context) (Order, error) {
		sc, err := c.boundContext()
		if err != nil {
			return Order{}, err
		}
		return c.orders.GetOrder(ctx, sc, id)
	})
}

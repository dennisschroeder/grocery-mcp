package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

const (
	defaultOrdersPageLimit = 20
	maxOrdersPageLimit     = 100
)

// Money and moneyOutput are defined in money.go, shared with basket.go.

type OrderSummary struct {
	ID         string `json:"id" jsonschema:"REWE order identifier"`
	Status     string `json:"status" jsonschema:"open, ready, collected, cancelled, or unknown"`
	StoreID    string `json:"store_id,omitempty" jsonschema:"REWE market identifier, empty if not reported"`
	PickupAt   string `json:"pickup_at,omitempty" jsonschema:"UTC pickup time in RFC 3339 format, empty if not reported"`
	Total      Money  `json:"total"`
	ObservedAt string `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

type OrderItem struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	Quantity  int    `json:"quantity"`
	UnitPrice Money  `json:"unit_price"`
	LineTotal Money  `json:"line_total"`
}

type OrdersListInput struct {
	Limit  int `json:"limit,omitempty" jsonschema:"maximum number of orders to return (default 20, max 100)"`
	Offset int `json:"offset,omitempty" jsonschema:"number of orders to skip"`
}

type OrdersListOutput struct {
	Orders     []OrderSummary `json:"orders"`
	HasMore    bool           `json:"has_more"`
	ObservedAt string         `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

type OrderGetInput struct {
	OrderID string `json:"order_id" jsonschema:"REWE order identifier"`
}

type OrderGetOutput struct {
	OrderSummary
	Items []OrderItem `json:"items" jsonschema:"line items, empty if REWE's response did not include them"`
}

func RegisterOrdersTools(server *mcp.Server, core *shopping.Core) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "orders_list",
		Description: "List past REWE pickup orders as summaries. Read-only; Phase 1 cannot place orders. Some fields are best-effort because this endpoint is not yet live-verified against a real account.",
		Annotations: annotations(true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input OrdersListInput) (*mcp.CallToolResult, OrdersListOutput, error) {
		page, err := core.ListOrders(ctx, normalizedPageRequest(input.Limit, input.Offset))
		if err != nil {
			return nil, OrdersListOutput{}, err
		}
		return nil, ordersListOutput(page), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "order_get",
		Description: "Get one REWE order's detail, including line items when REWE's response includes them. Read-only.",
		Annotations: annotations(true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input OrderGetInput) (*mcp.CallToolResult, OrderGetOutput, error) {
		order, err := core.GetOrder(ctx, shopping.OrderID(input.OrderID))
		if err != nil {
			return nil, OrderGetOutput{}, err
		}
		return nil, orderGetOutput(order), nil
	})
}

func normalizedPageRequest(limit, offset int) shopping.PageRequest {
	if limit <= 0 {
		limit = defaultOrdersPageLimit
	}
	if limit > maxOrdersPageLimit {
		limit = maxOrdersPageLimit
	}
	if offset < 0 {
		offset = 0
	}
	return shopping.PageRequest{Limit: limit, Offset: offset}
}

func formatObservedAt(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func orderSummaryOutput(o shopping.OrderSummary) OrderSummary {
	return OrderSummary{
		ID:         string(o.ID),
		Status:     string(o.Status),
		StoreID:    string(o.StoreID),
		PickupAt:   formatOptionalTime(o.PickupAt),
		Total:      moneyOutput(o.Total),
		ObservedAt: formatObservedAt(o.ObservedAt),
	}
}

func ordersListOutput(page shopping.OrderPage) OrdersListOutput {
	orders := make([]OrderSummary, 0, len(page.Orders))
	for _, order := range page.Orders {
		orders = append(orders, orderSummaryOutput(order))
	}
	return OrdersListOutput{Orders: orders, HasMore: page.HasMore, ObservedAt: formatObservedAt(page.ObservedAt)}
}

func orderItemOutput(item shopping.BasketItem) OrderItem {
	return OrderItem{
		ProductID: string(item.ProductID),
		Name:      item.Name,
		Quantity:  item.Quantity,
		UnitPrice: moneyOutput(item.UnitPrice),
		LineTotal: moneyOutput(item.LineTotal),
	}
}

func orderGetOutput(order shopping.Order) OrderGetOutput {
	items := make([]OrderItem, 0, len(order.Items))
	for _, item := range order.Items {
		items = append(items, orderItemOutput(item))
	}
	return OrderGetOutput{OrderSummary: orderSummaryOutput(order.OrderSummary), Items: items}
}

package rewe

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// ListOrders and GetOrder implement shopping.OrdersGateway. Neither
// orders_list nor order_get is live-tested against every field yet — field
// names below are the best available reading of karrt's independent
// research, not proven against a real account (orders_list's wrapper shape
// and "id" fields ARE confirmed live, 2026-08-19, 20 real orders). Only each
// payload's "id" is enforced as critical; every other field defaults
// gracefully when absent rather than failing the whole read over an
// optional field that turns out renamed. A wrong guess about the *document
// shape itself* (wrong JSON kind, missing id) still fails closed via
// shopping.UpstreamChangeError, the same lesson card #13's
// decodeFavoriteListID bug taught.
//
// receipts_list/receipt_get/GetReceipt were removed (product decision,
// 2026-08-19): live investigation found no REWE UI path exposing a digital
// receipts feature separate from order history — "Deine Einkäufe" and each
// order's own detail view are the only purchase-history surfaces REWE
// exposes, both already covered by ListOrders/GetOrder. karrt's own
// /receipts endpoint claim was never corroborated against real traffic,
// unlike orders_list/products_search/timeslots_list, which all were.

func (g Gateway) ListOrders(ctx context.Context, sc shopping.ShoppingContext, page shopping.PageRequest) (shopping.OrderPage, error) {
	result, err := g.Transport.Do(ctx, browserbridge.OperationOrdersList, nil)
	if err != nil {
		return shopping.OrderPage{}, classifyReadBridgeError("orders.list", err)
	}
	payload, err := decodeCritical[ordersListPayload]("orders.list", result)
	if err != nil {
		return shopping.OrderPage{}, err
	}
	observedAt := g.now().UTC()
	orders := make([]shopping.OrderSummary, 0, len(payload.Orders))
	for _, order := range payload.Orders {
		orders = append(orders, order.toDomain(observedAt))
	}
	paged, hasMore := paginate(orders, page)
	return shopping.OrderPage{Orders: paged, HasMore: hasMore, ObservedAt: observedAt}, nil
}

func (g Gateway) GetOrder(ctx context.Context, sc shopping.ShoppingContext, id shopping.OrderID) (shopping.Order, error) {
	if strings.TrimSpace(string(id)) == "" {
		return shopping.Order{}, &shopping.ValidationError{Operation: "orders.get", Field: "order_id", Problem: shopping.ValidationMissing}
	}
	params, err := json.Marshal(orderGetParams{OrderID: string(id)})
	if err != nil {
		return shopping.Order{}, err
	}
	result, err := g.Transport.Do(ctx, browserbridge.OperationOrderGet, params)
	if err != nil {
		return shopping.Order{}, classifyReadBridgeError("orders.get", err)
	}
	payload, err := decodeCritical[orderDetailPayload]("orders.get", unwrapOrderDetails(result))
	if err != nil {
		return shopping.Order{}, err
	}
	return payload.toDomain(g.now().UTC()), nil
}

type orderGetParams struct {
	OrderID string `json:"order_id"`
}

// classifyReadBridgeError is defined in bridge_errors.go, shared with the
// other two verticals.

// paginate applies PageRequest client-side over an already-decoded slice.
// orders_list research turned up no confirmed REWE-side pagination query
// parameters, so content-script.js fetches the endpoint's full response and
// pagination happens here instead of guessing at unconfirmed query params
// that could silently truncate results. Limit <= 0 means "no limit".
func paginate[T any](items []T, page shopping.PageRequest) ([]T, bool) {
	offset := page.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		offset = len(items)
	}
	if page.Limit <= 0 {
		return items[offset:], false
	}
	end := offset + page.Limit
	hasMore := end < len(items)
	if !hasMore {
		end = len(items)
	}
	return items[offset:end], hasMore
}

// unwrapOrderDetails resolves the order_get shape ambiguity the contract
// flags: some REWE responses nest the order under "orderDetails", others
// place it at the document root. Preferring a present, object-shaped
// "orderDetails" key over the root is exactly the kind of unverified-shape
// assumption that produced card #13's decodeFavoriteListID bug, so this
// function only ever *chooses* which object decodeCritical validates next —
// it never invents data, and an object that satisfies neither shape still
// fails closed via shopping.UpstreamMissingCriticalField/UpstreamTypeChanged.
func unwrapOrderDetails(body []byte) []byte {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return body
	}
	wrapped, ok := probe["orderDetails"]
	if !ok {
		return body
	}
	trimmed := bytes.TrimSpace(wrapped)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body
	}
	return wrapped
}

type moneyPayload struct {
	Cents    *int64  `json:"cents"`
	Currency *string `json:"currency"`
}

func (m moneyPayload) toMoney() shopping.Money {
	money := shopping.Money{Currency: "EUR"}
	if m.Cents != nil {
		money.Cents = *m.Cents
	}
	if m.Currency != nil && *m.Currency != "" {
		money.Currency = *m.Currency
	}
	return money
}

func parseUpstreamTime(raw *string) time.Time {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339, *raw); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339Nano, *raw); err == nil {
		return parsed
	}
	return time.Time{}
}

func parseOrderStatus(raw *string) shopping.OrderStatus {
	if raw == nil {
		return shopping.OrderStatusUnknown
	}
	switch strings.ToUpper(strings.TrimSpace(*raw)) {
	case "OPEN":
		return shopping.OrderStatusOpen
	case "READY":
		return shopping.OrderStatusReady
	case "COLLECTED":
		return shopping.OrderStatusCollected
	case "CANCELLED", "CANCELED":
		return shopping.OrderStatusCancelled
	default:
		return shopping.OrderStatusUnknown
	}
}

// ordersListPayload assumes GET /orders wraps its array under "orders",
// mirroring products_search's confirmed {"products": [...]} shape (the only
// live-tested list endpoint on hand) rather than favorites' unwrapped
// top-level array — unconfirmed either way. If REWE actually returns a bare
// array, decoding into this struct's top-level object fails closed with
// shopping.UpstreamTypeChanged instead of silently reporting zero orders.
type ordersListPayload struct {
	Orders []orderSummaryPayload `json:"orders"`
}

func (ordersListPayload) criticalFields() []string { return []string{"orders"} }

func (p ordersListPayload) validate() shopping.UpstreamProblem {
	for _, order := range p.Orders {
		if problem := order.validate(); problem != "" {
			return problem
		}
	}
	return ""
}

type orderSummaryPayload struct {
	ID       *string      `json:"id"`
	Status   *string      `json:"status"`
	MarketID *string      `json:"marketId"`
	PickupAt *string      `json:"pickupAt"`
	Total    moneyPayload `json:"total"`
}

func (p orderSummaryPayload) validate() shopping.UpstreamProblem {
	if p.ID == nil || strings.TrimSpace(*p.ID) == "" {
		return shopping.UpstreamMissingCriticalField
	}
	return ""
}

func (p orderSummaryPayload) toDomain(observedAt time.Time) shopping.OrderSummary {
	var storeID shopping.StoreID
	if p.MarketID != nil {
		storeID = shopping.StoreID(*p.MarketID)
	}
	return shopping.OrderSummary{
		ID:         shopping.OrderID(*p.ID),
		Status:     parseOrderStatus(p.Status),
		StoreID:    storeID,
		PickupAt:   parseUpstreamTime(p.PickupAt),
		Total:      p.Total.toMoney(),
		ObservedAt: observedAt,
	}
}

// orderDetailPayload is decoded from whichever object unwrapOrderDetails
// selected (the "orderDetails" wrapper or the response root).
type orderDetailPayload struct {
	ID       *string            `json:"id"`
	Status   *string            `json:"status"`
	MarketID *string            `json:"marketId"`
	PickupAt *string            `json:"pickupAt"`
	Total    moneyPayload       `json:"total"`
	Items    []orderItemPayload `json:"items"`
}

func (orderDetailPayload) criticalFields() []string { return []string{"id"} }

func (p orderDetailPayload) validate() shopping.UpstreamProblem {
	if p.ID == nil || strings.TrimSpace(*p.ID) == "" {
		return shopping.UpstreamMissingCriticalField
	}
	return ""
}

func (p orderDetailPayload) toDomain(observedAt time.Time) shopping.Order {
	var storeID shopping.StoreID
	if p.MarketID != nil {
		storeID = shopping.StoreID(*p.MarketID)
	}
	items := make([]shopping.BasketItem, 0, len(p.Items))
	for _, item := range p.Items {
		items = append(items, item.toDomain())
	}
	return shopping.Order{
		OrderSummary: shopping.OrderSummary{
			ID:         shopping.OrderID(*p.ID),
			Status:     parseOrderStatus(p.Status),
			StoreID:    storeID,
			PickupAt:   parseUpstreamTime(p.PickupAt),
			Total:      p.Total.toMoney(),
			ObservedAt: observedAt,
		},
		Items: items,
	}
}

type orderItemPayload struct {
	ProductID *string      `json:"productId"`
	Name      *string      `json:"name"`
	Quantity  *int         `json:"quantity"`
	UnitPrice moneyPayload `json:"unitPrice"`
	LineTotal moneyPayload `json:"lineTotal"`
}

func (p orderItemPayload) toDomain() shopping.BasketItem {
	item := shopping.BasketItem{
		UnitPrice: p.UnitPrice.toMoney(),
		LineTotal: p.LineTotal.toMoney(),
	}
	if p.ProductID != nil {
		item.ProductID = shopping.ProductID(*p.ProductID)
	}
	if p.Name != nil {
		item.Name = *p.Name
	}
	if p.Quantity != nil {
		item.Quantity = *p.Quantity
	}
	return item
}

package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// Money and moneyOutput are defined in money.go, shared with orders.go.

type BasketItem struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	Quantity  int    `json:"quantity"`
	UnitPrice Money  `json:"unit_price"`
	LineTotal Money  `json:"line_total"`
}

type Basket struct {
	ID         string       `json:"id"`
	StoreID    string       `json:"store_id"`
	Items      []BasketItem `json:"items"`
	Subtotal   Money        `json:"subtotal"`
	Fees       Money        `json:"fees"`
	Total      Money        `json:"total"`
	TimeSlotID string       `json:"timeslot_id,omitempty"`
	ObservedAt string       `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

type BasketChangeInput struct {
	ProductID string `json:"product_id" jsonschema:"REWE product identifier to add, update, or remove"`
	Quantity  int    `json:"quantity" jsonschema:"desired quantity for this product; 0 removes it from the basket"`
}

type BasketApplyInput struct {
	Changes []BasketChangeInput `json:"changes" jsonschema:"one or more line-item changes to apply to the basket"`
}

type BasketChangeOutcome struct {
	ProductID         string `json:"product_id"`
	RequestedQuantity int    `json:"requested_quantity"`
	AppliedQuantity   int    `json:"applied_quantity"`
	Status            string `json:"status"`
	Problem           string `json:"problem,omitempty"`
}

type BasketApplyOutput struct {
	Basket   Basket                `json:"basket"`
	Outcomes []BasketChangeOutcome `json:"outcomes"`
}

type TimeSlot struct {
	ID         string `json:"id"`
	StoreID    string `json:"store_id"`
	StartsAt   string `json:"starts_at" jsonschema:"UTC start time in RFC 3339 format"`
	EndsAt     string `json:"ends_at" jsonschema:"UTC end time in RFC 3339 format"`
	Fee        Money  `json:"fee"`
	Available  bool   `json:"available"`
	ObservedAt string `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

type TimeSlotListOutput struct {
	TimeSlots  []TimeSlot `json:"timeslots"`
	ObservedAt string     `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

type TimeSlotSelectInput struct {
	TimeSlotID string `json:"timeslot_id" jsonschema:"identifier of the pickup timeslot to reserve, as returned by timeslots_list"`
}

type TimeSlotSelectOutput struct {
	StoreID    string `json:"store_id,omitempty"`
	BasketID   string `json:"basket_id,omitempty"`
	TimeSlotID string `json:"timeslot_id,omitempty"`
}

// RegisterBasketTools registers the basket/timeslot vertical's MCP tools.
// Callers wire this into mcpserver.New's server during fan-in — this file
// never touches server.go itself.
func RegisterBasketTools(server *mcp.Server, core *shopping.Core) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "basket_get",
		Description: "Return the current REWE basket. Fails with a validation error until at least one item has been added, since REWE has no basket to read before that.",
		Annotations: annotations(true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, Basket, error) {
		basket, err := core.GetBasket(ctx)
		if err != nil {
			return nil, Basket{}, err
		}
		return nil, basketOutput(basket), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "basket_apply",
		Description: "Add, update, or remove basket line items (quantity 0 removes). REWE has no bulk endpoint, so each change is applied independently — one item failing does not fail the others; check each outcome's status.",
		Annotations: mutationAnnotations(false, true, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in BasketApplyInput) (*mcp.CallToolResult, BasketApplyOutput, error) {
		changes := make([]shopping.BasketChange, 0, len(in.Changes))
		for _, change := range in.Changes {
			changes = append(changes, shopping.BasketChange{ProductID: shopping.ProductID(change.ProductID), Quantity: change.Quantity})
		}
		result, err := core.ApplyBasket(ctx, shopping.BasketMutation{Changes: changes})
		if err != nil {
			return nil, BasketApplyOutput{}, err
		}
		return nil, basketApplyOutput(result), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "timeslots_list",
		Description: "List available REWE pickup timeslots for the selected store.",
		Annotations: annotations(true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, TimeSlotListOutput, error) {
		list, err := core.ListTimeSlots(ctx)
		if err != nil {
			return nil, TimeSlotListOutput{}, err
		}
		return nil, timeSlotListOutput(list), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "timeslot_select",
		// Not yet functional: REWE's reservation call needs a customerId
		// this project has no verified source for (see gateway_basket.go's
		// SelectTimeSlot). Documented here rather than left silent, so a
		// caller sees why every call currently fails.
		Description: "Reserve a pickup timeslot for the current basket. Currently always fails with a validation error: REWE's timeslot-reservations endpoint requires a customer id this project cannot yet source without live REWE testing.",
		Annotations: mutationAnnotations(false, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in TimeSlotSelectInput) (*mcp.CallToolResult, TimeSlotSelectOutput, error) {
		next, err := core.SelectTimeSlot(ctx, shopping.TimeSlotID(in.TimeSlotID))
		if err != nil {
			return nil, TimeSlotSelectOutput{}, err
		}
		return nil, TimeSlotSelectOutput{
			StoreID:    string(next.StoreID),
			BasketID:   string(next.BasketID),
			TimeSlotID: string(next.TimeSlotID),
		}, nil
	})
}

// mutationAnnotations builds on the shared annotations() helper (owned by
// server.go, not edited here) but corrects IdempotentHint: annotations()
// hardcodes it true, which is wrong for basket_apply/timeslot_select —
// applying the same change twice, or reserving twice, is not guaranteed to
// be a no-op.
func mutationAnnotations(readOnly, destructive, openWorld bool) *mcp.ToolAnnotations {
	toolAnnotations := annotations(readOnly, destructive, openWorld)
	toolAnnotations.IdempotentHint = false
	return toolAnnotations
}

func basketOutput(basket shopping.Basket) Basket {
	items := make([]BasketItem, 0, len(basket.Items))
	for _, item := range basket.Items {
		items = append(items, BasketItem{
			ProductID: string(item.ProductID),
			Name:      item.Name,
			Quantity:  item.Quantity,
			UnitPrice: moneyOutput(item.UnitPrice),
			LineTotal: moneyOutput(item.LineTotal),
		})
	}
	return Basket{
		ID:         string(basket.ID),
		StoreID:    string(basket.StoreID),
		Items:      items,
		Subtotal:   moneyOutput(basket.Subtotal),
		Fees:       moneyOutput(basket.Fees),
		Total:      moneyOutput(basket.Total),
		TimeSlotID: string(basket.TimeSlotID),
		ObservedAt: basket.ObservedAt.UTC().Format(time.RFC3339Nano),
	}
}

func basketApplyOutput(result shopping.BasketMutationResult) BasketApplyOutput {
	outcomes := make([]BasketChangeOutcome, 0, len(result.Outcomes))
	for _, outcome := range result.Outcomes {
		outcomes = append(outcomes, BasketChangeOutcome{
			ProductID:         string(outcome.ProductID),
			RequestedQuantity: outcome.RequestedQuantity,
			AppliedQuantity:   outcome.AppliedQuantity,
			Status:            string(outcome.Status),
			Problem:           string(outcome.Problem),
		})
	}
	return BasketApplyOutput{Basket: basketOutput(result.Basket), Outcomes: outcomes}
}

func timeSlotOutput(slot shopping.TimeSlot) TimeSlot {
	return TimeSlot{
		ID:         string(slot.ID),
		StoreID:    string(slot.StoreID),
		StartsAt:   slot.StartsAt.UTC().Format(time.RFC3339Nano),
		EndsAt:     slot.EndsAt.UTC().Format(time.RFC3339Nano),
		Fee:        moneyOutput(slot.Fee),
		Available:  slot.Available,
		ObservedAt: slot.ObservedAt.UTC().Format(time.RFC3339Nano),
	}
}

func timeSlotListOutput(list shopping.TimeSlotList) TimeSlotListOutput {
	slots := make([]TimeSlot, 0, len(list.TimeSlots))
	for _, slot := range list.TimeSlots {
		slots = append(slots, timeSlotOutput(slot))
	}
	return TimeSlotListOutput{TimeSlots: slots, ObservedAt: list.ObservedAt.UTC().Format(time.RFC3339Nano)}
}

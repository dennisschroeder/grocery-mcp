package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type CheckoutApproval struct {
	ApprovalID    string `json:"approval_id"`
	Status        string `json:"status" jsonschema:"pending, approved, declined, expired, invalidated, committed, or commit_failed"`
	Basket        Basket `json:"basket"`
	StoreID       string `json:"store_id"`
	TimeSlotID    string `json:"timeslot_id"`
	ApprovalURL   string `json:"approval_url,omitempty" jsonschema:"read-only link to a local page a human must open, review, and submit themselves; this URL alone cannot approve or decline anything, and it must never be POSTed to directly or scripted"`
	CreatedAt     string `json:"created_at" jsonschema:"UTC observation time in RFC 3339 format"`
	ExpiresAt     string `json:"expires_at" jsonschema:"UTC observation time in RFC 3339 format"`
	OrderID       string `json:"order_id,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
}

func checkoutApprovalOutput(approval shopping.CheckoutApproval) CheckoutApproval {
	return CheckoutApproval{
		ApprovalID:    string(approval.ID),
		Status:        string(approval.Status),
		Basket:        basketOutput(approval.Basket),
		StoreID:       string(approval.StoreID),
		TimeSlotID:    string(approval.TimeSlotID),
		ApprovalURL:   approval.ApprovalURL,
		CreatedAt:     formatObservedAt(approval.CreatedAt),
		ExpiresAt:     formatObservedAt(approval.ExpiresAt),
		OrderID:       string(approval.OrderID),
		FailureReason: approval.FailureReason,
	}
}

type OrderStatusInput struct {
	ApprovalID string `json:"approval_id" jsonschema:"the approval_id returned by order_prepare"`
}

func RegisterCheckoutTools(server *mcp.Server, core *shopping.Core) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "order_prepare",
		// AGENTS.md: every order needs a fresh, out-of-band human approval
		// bound to the exact basket, price, store, and timeslot. This tool
		// only creates that approval — it never places an order, and no
		// commit path is reachable through this MCP server's tools at all.
		Description: "Reload the authoritative basket, store, and pickup timeslot, and create a short-lived, human-approvable order. No order is placed by this call: a human must open approval_url themselves and explicitly approve before anything is committed. Calling this again supersedes any prior unresolved approval.",
		Annotations: mutationAnnotations(false, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, CheckoutApproval, error) {
		approval, err := core.PrepareOrder(ctx)
		if err != nil {
			return nil, CheckoutApproval{}, err
		}
		return nil, checkoutApprovalOutput(approval), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "order_status",
		Description: "Check a prepared order's approval status. Status only changes through explicit human action on the approval page, or automatically on expiry or a basket/store/timeslot change since order_prepare — never through this call.",
		Annotations: annotations(true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in OrderStatusInput) (*mcp.CallToolResult, CheckoutApproval, error) {
		approval, err := core.OrderStatus(ctx, shopping.ApprovalID(in.ApprovalID))
		if err != nil {
			return nil, CheckoutApproval{}, err
		}
		return nil, checkoutApprovalOutput(approval), nil
	})
}

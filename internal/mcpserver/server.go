package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

const version = "0.1.0"

type EmptyInput struct{}

type AuthStatus struct {
	State           string `json:"state" jsonschema:"current authentication state"`
	ActionRequired  bool   `json:"action_required" jsonschema:"whether a human action is required"`
	Instruction     string `json:"instruction,omitempty" jsonschema:"safe human instruction when action is required"`
	SessionRevision uint64 `json:"session_revision" jsonschema:"accepted in-memory session revision; zero when disconnected"`
	ObservedAt      string `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

func New(core *shopping.Core) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "grocery-mcp", Version: version}, &mcp.ServerOptions{
		Instructions: "No tool on this server places an order. order_prepare only creates a human-reviewable approval; committing it requires an explicit human action on a local approval page, never a tool call. Authentication material never appears in tool input or output.",
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "auth_connect",
		Description: "Begin browser-assisted REWE authentication. When action_required is true, the human clicks the grocery-mcp Chrome extension.",
		Annotations: annotations(false, false, true),
	}, func(context.Context, *mcp.CallToolRequest, EmptyInput) (*mcp.CallToolResult, AuthStatus, error) {
		return nil, statusOutput(core.AuthConnect()), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "auth_status",
		Description: "Return the sanitized authentication state without account or credential data.",
		Annotations: annotations(true, false, false),
	}, func(context.Context, *mcp.CallToolRequest, EmptyInput) (*mcp.CallToolResult, AuthStatus, error) {
		return nil, statusOutput(core.AuthStatus()), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "auth_disconnect",
		Description: "Destroy the in-memory REWE credential and shopping session.",
		Annotations: annotations(false, true, false),
	}, func(context.Context, *mcp.CallToolRequest, EmptyInput) (*mcp.CallToolResult, AuthStatus, error) {
		return nil, statusOutput(core.AuthDisconnect()), nil
	})

	RegisterStoresTools(server, core)
	RegisterBasketTools(server, core)
	RegisterOrdersTools(server, core)
	RegisterCheckoutTools(server, core)

	return server
}

func statusOutput(status shopping.AuthStatus) AuthStatus {
	instruction := ""
	if status.ActionRequired {
		switch status.State {
		case shopping.AuthReauthRequired:
			instruction = "Log into REWE in Chrome, then click the grocery-mcp extension."
		default:
			instruction = "Click the grocery-mcp extension in signed-in Chrome."
		}
	}
	return AuthStatus{
		State:           string(status.State),
		ActionRequired:  status.ActionRequired,
		Instruction:     instruction,
		SessionRevision: status.SessionRevision,
		ObservedAt:      status.ObservedAt.UTC().Format(time.RFC3339Nano),
	}
}

func annotations(readOnly, destructive, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  true,
		OpenWorldHint:   &openWorld,
	}
}

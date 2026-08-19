package mcpserver

import (
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/auth"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

func TestAuthToolsAreReachableThroughMCP(t *testing.T) {
	ctx := t.Context()
	service := auth.NewService(nil)
	server := New(shopping.NewCore(service, nil, nil, nil))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "grocery-mcp-test", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{
		"auth_connect", "auth_disconnect", "auth_status",
		"basket_apply", "basket_get",
		"order_get", "orders_list",
		"products_search",
		"store_select", "stores_search",
		"timeslot_select", "timeslots_list",
	}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("tools are %v, want %v", names, want)
	}

	if _, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "auth_connect"}); err != nil {
		t.Fatal(err)
	}
	if service.Status().State != auth.StateBootstrapping {
		t.Fatalf("state is %s", service.Status().State)
	}
	if _, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "auth_status"}); err != nil {
		t.Fatal(err)
	}
	if _, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "auth_disconnect"}); err != nil {
		t.Fatal(err)
	}
	if service.Status().State != auth.StateUnauthenticated {
		t.Fatalf("state is %s", service.Status().State)
	}
}

func TestAuthToolAnnotationsAreExplicit(t *testing.T) {
	server := New(shopping.NewCore(auth.NewService(nil), nil, nil, nil))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "grocery-mcp-test", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	defer serverSession.Close()
	listed, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// basket_apply and timeslot_select are real mutations without a proven
	// idempotency guarantee (AGENTS.md: mutations need proven idempotency or
	// reconciliation before any retry) — mutationAnnotations marks them
	// non-idempotent on purpose. Every other tool, including the other
	// selection/mutation-shaped ones, is expected idempotent.
	notIdempotent := map[string]bool{"basket_apply": true, "timeslot_select": true}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || tool.Annotations.OpenWorldHint == nil {
			t.Fatalf("%s has incomplete annotations", tool.Name)
		}
		readOnly := tool.Annotations.ReadOnlyHint
		destructive := *tool.Annotations.DestructiveHint
		openWorld := *tool.Annotations.OpenWorldHint
		if tool.Annotations.IdempotentHint == notIdempotent[tool.Name] {
			t.Fatalf("%s idempotent hint is %v, want %v", tool.Name, tool.Annotations.IdempotentHint, !notIdempotent[tool.Name])
		}
		switch tool.Name {
		case "auth_connect":
			if readOnly || destructive || !openWorld {
				t.Fatalf("unexpected auth_connect annotations: %#v", tool.Annotations)
			}
		case "auth_status":
			if !readOnly || destructive || openWorld {
				t.Fatalf("unexpected auth_status annotations: %#v", tool.Annotations)
			}
		case "auth_disconnect":
			if readOnly || !destructive || openWorld {
				t.Fatalf("unexpected auth_disconnect annotations: %#v", tool.Annotations)
			}
		}
	}
}

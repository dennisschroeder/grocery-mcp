package mcpserver

import (
	"context"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/auth"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// unreachableStoresGateway panics if ever called — used to prove
// Core.boundContext fails closed before any gateway call is reached.
type unreachableStoresGateway struct{}

func (unreachableStoresGateway) SearchStores(context.Context, shopping.ShoppingContext, shopping.StoreSearch) (shopping.StorePage, error) {
	panic("gateway reached without authentication")
}

func (unreachableStoresGateway) SelectStore(context.Context, shopping.ShoppingContext, shopping.StoreID) (shopping.ShoppingContext, error) {
	panic("gateway reached without authentication")
}

func (unreachableStoresGateway) SearchProducts(context.Context, shopping.ShoppingContext, shopping.ProductSearch) (shopping.ProductPage, error) {
	panic("gateway reached without authentication")
}

func newStoresTestSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	core := shopping.NewCore(auth.NewService(nil), unreachableStoresGateway{}, nil, nil, nil)
	server := mcp.NewServer(&mcp.Implementation{Name: "grocery-mcp-test", Version: "0.0.0"}, nil)
	RegisterStoresTools(server, core)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "grocery-mcp-test-client", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	return clientSession
}

func TestStoresToolsAreReachableThroughMCP(t *testing.T) {
	client := newStoresTestSession(t)
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"products_search", "store_select", "stores_search"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools are %v, want %v", names, want)
	}
}

func TestStoresToolAnnotationsAreExplicit(t *testing.T) {
	client := newStoresTestSession(t)
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || tool.Annotations.OpenWorldHint == nil {
			t.Fatalf("%s has incomplete annotations", tool.Name)
		}
		readOnly := tool.Annotations.ReadOnlyHint
		destructive := *tool.Annotations.DestructiveHint
		openWorld := *tool.Annotations.OpenWorldHint
		if !tool.Annotations.IdempotentHint {
			t.Fatalf("%s is not marked idempotent", tool.Name)
		}
		switch tool.Name {
		case "stores_search", "products_search":
			if !readOnly || destructive || !openWorld {
				t.Fatalf("unexpected %s annotations: %#v", tool.Name, tool.Annotations)
			}
		case "store_select":
			if readOnly || destructive || openWorld {
				t.Fatalf("unexpected store_select annotations: %#v", tool.Annotations)
			}
		}
	}
}

func TestStoresSearchFailsClosedWhenUnauthenticated(t *testing.T) {
	client := newStoresTestSession(t)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "stores_search",
		Arguments: map[string]any{"postal_code": "10115"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected stores_search to fail while unauthenticated")
	}
}

func TestStoreSelectFailsClosedWhenUnauthenticated(t *testing.T) {
	client := newStoresTestSession(t)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "store_select",
		Arguments: map[string]any{"store_id": "123456"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected store_select to fail while unauthenticated")
	}
}

func TestProductsSearchFailsClosedWhenUnauthenticated(t *testing.T) {
	client := newStoresTestSession(t)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "products_search",
		Arguments: map[string]any{"query": "milch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected products_search to fail while unauthenticated")
	}
}

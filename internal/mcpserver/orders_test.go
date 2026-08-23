package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// stubAuthenticator and stubOrdersGateway are narrow test doubles (mirrors
// internal/shopping's core_orders_test.go doubles) — this vertical's tests
// construct their own *shopping.Core rather than depending on stores/basket
// gateways, which don't exist in this isolated worktree.
type stubAuthenticator struct {
	identity shopping.SessionIdentity
}

func (a *stubAuthenticator) Connect() shopping.AuthStatus               { return shopping.AuthStatus{} }
func (a *stubAuthenticator) Status() shopping.AuthStatus                { return shopping.AuthStatus{} }
func (a *stubAuthenticator) Disconnect() shopping.AuthStatus            { return shopping.AuthStatus{} }
func (a *stubAuthenticator) Identity() (shopping.SessionIdentity, bool) { return a.identity, true }
func (a *stubAuthenticator) RefreshAndValidate(context.Context) error   { return nil }

type stubOrdersGateway struct {
	orderPage shopping.OrderPage
	order     shopping.Order
	err       error
}

func (g *stubOrdersGateway) ListOrders(context.Context, shopping.ShoppingContext, shopping.PageRequest) (shopping.OrderPage, error) {
	return g.orderPage, g.err
}

func (g *stubOrdersGateway) GetOrder(context.Context, shopping.ShoppingContext, shopping.OrderID) (shopping.Order, error) {
	return g.order, g.err
}

func connectedOrdersTestServer(t *testing.T, orders *stubOrdersGateway) *mcp.ClientSession {
	t.Helper()
	auth := &stubAuthenticator{identity: shopping.SessionIdentity{AccountID: "account-1"}}
	core := shopping.NewCore(auth, nil, nil, orders, nil)
	server := mcp.NewServer(&mcp.Implementation{Name: "grocery-mcp-test", Version: "0.0.0"}, nil)
	RegisterOrdersTools(server, core)

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

func TestOrdersToolsAreReachableThroughMCP(t *testing.T) {
	client := connectedOrdersTestServer(t, &stubOrdersGateway{})
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"order_get", "orders_list"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools are %v, want %v", names, want)
	}
}

func TestOrdersToolAnnotationsAreReadOnly(t *testing.T) {
	client := connectedOrdersTestServer(t, &stubOrdersGateway{})
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil {
			t.Fatalf("%s has no annotations", tool.Name)
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s is not marked read-only", tool.Name)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("%s is marked destructive", tool.Name)
		}
	}
}

func TestOrdersListReturnsMoneyAndObservedAt(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	orders := &stubOrdersGateway{orderPage: shopping.OrderPage{
		Orders: []shopping.OrderSummary{{
			ID:         "order-1",
			Status:     shopping.OrderStatusReady,
			StoreID:    "market-1",
			PickupAt:   observedAt,
			Total:      shopping.Money{Cents: 2599, Currency: "EUR"},
			ObservedAt: observedAt,
		}},
		ObservedAt: observedAt,
	}}
	client := connectedOrdersTestServer(t, orders)

	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "orders_list"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool call failed: %+v", result.Content)
	}
	var output OrdersListOutput
	decodeStructuredContent(t, result, &output)
	if len(output.Orders) != 1 {
		t.Fatalf("Orders = %#v", output.Orders)
	}
	order := output.Orders[0]
	if order.Total.Cents != 2599 || order.Total.Currency != "EUR" {
		t.Fatalf("Total = %#v", order.Total)
	}
	if order.ObservedAt != observedAt.Format(time.RFC3339Nano) {
		t.Fatalf("ObservedAt = %q, want %q", order.ObservedAt, observedAt.Format(time.RFC3339Nano))
	}
}

func decodeStructuredContent(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
}

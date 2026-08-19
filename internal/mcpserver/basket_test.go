package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// stubBasketAuthenticator and stubBasketGateway are narrow test doubles
// (this vertical's own, not shared with server_test.go's auth-tool tests)
// so basket_get/basket_apply/timeslots_list/timeslot_select can be exercised
// end to end through a real in-memory MCP session without any REWE gateway.
type stubBasketAuthenticator struct{}

func (stubBasketAuthenticator) Connect() shopping.AuthStatus    { return shopping.AuthStatus{} }
func (stubBasketAuthenticator) Status() shopping.AuthStatus     { return shopping.AuthStatus{} }
func (stubBasketAuthenticator) Disconnect() shopping.AuthStatus { return shopping.AuthStatus{} }
func (stubBasketAuthenticator) Identity() (shopping.SessionIdentity, bool) {
	return shopping.SessionIdentity{AccountID: "account-1"}, true
}
func (stubBasketAuthenticator) RefreshAndValidate(context.Context) error { return nil }

type stubBasketGateway struct {
	getBasket      func(context.Context, shopping.ShoppingContext) (shopping.Basket, error)
	applyBasket    func(context.Context, shopping.ShoppingContext, shopping.BasketMutation) (shopping.BasketMutationResult, error)
	listTimeSlots  func(context.Context, shopping.ShoppingContext) (shopping.TimeSlotList, error)
	selectTimeSlot func(context.Context, shopping.ShoppingContext, shopping.TimeSlotID) (shopping.ShoppingContext, error)
}

func (g stubBasketGateway) GetBasket(ctx context.Context, sc shopping.ShoppingContext) (shopping.Basket, error) {
	return g.getBasket(ctx, sc)
}

func (g stubBasketGateway) ApplyBasket(ctx context.Context, sc shopping.ShoppingContext, mutation shopping.BasketMutation) (shopping.BasketMutationResult, error) {
	return g.applyBasket(ctx, sc, mutation)
}

func (g stubBasketGateway) ListTimeSlots(ctx context.Context, sc shopping.ShoppingContext) (shopping.TimeSlotList, error) {
	return g.listTimeSlots(ctx, sc)
}

func (g stubBasketGateway) SelectTimeSlot(ctx context.Context, sc shopping.ShoppingContext, id shopping.TimeSlotID) (shopping.ShoppingContext, error) {
	return g.selectTimeSlot(ctx, sc, id)
}

func newBasketTestSession(t *testing.T, gateway shopping.BasketGateway) *mcp.ClientSession {
	t.Helper()
	core := shopping.NewCore(stubBasketAuthenticator{}, nil, gateway, nil)
	server := mcp.NewServer(&mcp.Implementation{Name: "grocery-mcp-test", Version: "0.0.0"}, nil)
	RegisterBasketTools(server, core)

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

func structuredContent[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	var out T
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	return out
}

func TestBasketToolsAreReachableThroughMCP(t *testing.T) {
	session := newBasketTestSession(t, stubBasketGateway{})
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"basket_apply", "basket_get", "timeslot_select", "timeslots_list"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools are %v, want %v", names, want)
	}
}

func TestBasketToolAnnotationsReflectMutability(t *testing.T) {
	session := newBasketTestSession(t, stubBasketGateway{})
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || tool.Annotations.OpenWorldHint == nil {
			t.Fatalf("%s has incomplete annotations", tool.Name)
		}
		switch tool.Name {
		case "basket_get", "timeslots_list":
			if !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
				t.Fatalf("%s should be read-only and idempotent: %#v", tool.Name, tool.Annotations)
			}
		case "basket_apply", "timeslot_select":
			if tool.Annotations.ReadOnlyHint {
				t.Fatalf("%s must not be read-only", tool.Name)
			}
			if tool.Annotations.IdempotentHint {
				t.Fatalf("%s must not claim idempotency", tool.Name)
			}
		}
	}
}

func TestBasketGetToolReturnsBasketOutput(t *testing.T) {
	basket := shopping.Basket{
		ID:      "basket-1",
		StoreID: "660500",
		Items: []shopping.BasketItem{
			{ProductID: "p1", Name: "Milk", Quantity: 2, UnitPrice: shopping.Money{Cents: 100, Currency: "EUR"}, LineTotal: shopping.Money{Cents: 200, Currency: "EUR"}},
		},
		Subtotal: shopping.Money{Cents: 200, Currency: "EUR"},
		Fees:     shopping.Money{Cents: 0, Currency: "EUR"},
		Total:    shopping.Money{Cents: 200, Currency: "EUR"},
	}
	gateway := stubBasketGateway{
		getBasket: func(context.Context, shopping.ShoppingContext) (shopping.Basket, error) { return basket, nil },
	}
	session := newBasketTestSession(t, gateway)
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "basket_get"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("basket_get returned an error result: %+v", result.Content)
	}
	got := structuredContent[Basket](t, result)
	if got.ID != "basket-1" || got.StoreID != "660500" {
		t.Fatalf("basket_get output = %#v", got)
	}
	if len(got.Items) != 1 || got.Items[0].UnitPrice.Cents != 100 || got.Items[0].UnitPrice.Currency != "EUR" {
		t.Fatalf("basket_get items = %#v", got.Items)
	}
}

func TestBasketGetToolSurfacesValidationErrorWhenNoBasket(t *testing.T) {
	gateway := stubBasketGateway{
		getBasket: func(context.Context, shopping.ShoppingContext) (shopping.Basket, error) {
			return shopping.Basket{}, &shopping.ValidationError{Operation: "get basket", Field: "basket_id", Problem: shopping.ValidationMissing}
		},
	}
	session := newBasketTestSession(t, gateway)
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "basket_get"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected basket_get to surface a tool error")
	}
}

func TestBasketApplyToolReturnsPartialOutcomes(t *testing.T) {
	gateway := stubBasketGateway{
		applyBasket: func(_ context.Context, _ shopping.ShoppingContext, mutation shopping.BasketMutation) (shopping.BasketMutationResult, error) {
			if len(mutation.Changes) != 2 {
				t.Fatalf("unexpected changes: %#v", mutation.Changes)
			}
			return shopping.BasketMutationResult{
				Basket: shopping.Basket{ID: "basket-1"},
				Outcomes: []shopping.BasketChangeOutcome{
					{ProductID: "p1", RequestedQuantity: 2, AppliedQuantity: 2, Status: shopping.BasketChangeApplied},
					{ProductID: "p2", RequestedQuantity: 0, AppliedQuantity: 0, Status: shopping.BasketChangeRejected, Problem: shopping.BasketProblemUnknown},
				},
			}, nil
		},
	}
	session := newBasketTestSession(t, gateway)
	arguments, err := json.Marshal(BasketApplyInput{Changes: []BasketChangeInput{{ProductID: "p1", Quantity: 2}, {ProductID: "p2", Quantity: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "basket_apply", Arguments: json.RawMessage(arguments)})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("basket_apply returned an error result: %+v", result.Content)
	}
	got := structuredContent[BasketApplyOutput](t, result)
	if len(got.Outcomes) != 2 || got.Outcomes[1].Status != string(shopping.BasketChangeRejected) {
		t.Fatalf("basket_apply outcomes = %#v", got.Outcomes)
	}
}

func TestTimeSlotsListToolReturnsTimeSlots(t *testing.T) {
	gateway := stubBasketGateway{
		listTimeSlots: func(context.Context, shopping.ShoppingContext) (shopping.TimeSlotList, error) {
			return shopping.TimeSlotList{TimeSlots: []shopping.TimeSlot{{ID: "slot-1", Available: true, Fee: shopping.Money{Currency: "EUR"}}}}, nil
		},
	}
	session := newBasketTestSession(t, gateway)
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "timeslots_list"})
	if err != nil {
		t.Fatal(err)
	}
	got := structuredContent[TimeSlotListOutput](t, result)
	if len(got.TimeSlots) != 1 || got.TimeSlots[0].ID != "slot-1" || !got.TimeSlots[0].Available {
		t.Fatalf("timeslots_list output = %#v", got)
	}
}

func TestTimeSlotSelectToolSurfacesTheStubbedValidationError(t *testing.T) {
	gateway := stubBasketGateway{
		selectTimeSlot: func(context.Context, shopping.ShoppingContext, shopping.TimeSlotID) (shopping.ShoppingContext, error) {
			return shopping.ShoppingContext{}, &shopping.ValidationError{Operation: "select timeslot", Field: "customer_id", Problem: shopping.ValidationMissing}
		},
	}
	session := newBasketTestSession(t, gateway)
	arguments, err := json.Marshal(TimeSlotSelectInput{TimeSlotID: "slot-1"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "timeslot_select", Arguments: json.RawMessage(arguments)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected timeslot_select to surface a tool error while customer_id is unresolved")
	}
}

package mcpserver

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/checkout"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type stubCheckoutBasketGateway struct {
	basket shopping.Basket
}

func (g stubCheckoutBasketGateway) GetBasket(context.Context, shopping.ShoppingContext) (shopping.Basket, error) {
	return g.basket, nil
}
func (stubCheckoutBasketGateway) GetBasketListings(context.Context, shopping.ShoppingContext) (shopping.BasketListingSnapshot, error) {
	return shopping.BasketListingSnapshot{}, nil
}
func (g stubCheckoutBasketGateway) ApplyBasket(context.Context, shopping.ShoppingContext, shopping.BasketMutation) (shopping.BasketMutationResult, error) {
	return shopping.BasketMutationResult{Basket: g.basket}, nil
}
func (stubCheckoutBasketGateway) ListTimeSlots(context.Context, shopping.ShoppingContext) (shopping.TimeSlotList, error) {
	return shopping.TimeSlotList{}, nil
}
func (stubCheckoutBasketGateway) SelectTimeSlot(_ context.Context, sc shopping.ShoppingContext, id shopping.TimeSlotID) (shopping.ShoppingContext, error) {
	return sc.WithTimeSlot(id), nil
}

type stubCheckoutStoresGateway struct{}

func (stubCheckoutStoresGateway) SearchStores(context.Context, shopping.ShoppingContext, shopping.StoreSearch) (shopping.StorePage, error) {
	return shopping.StorePage{}, nil
}
func (stubCheckoutStoresGateway) SelectStore(_ context.Context, sc shopping.ShoppingContext, id shopping.StoreID) (shopping.ShoppingContext, error) {
	return sc.WithStore(id), nil
}
func (stubCheckoutStoresGateway) SearchProducts(context.Context, shopping.ShoppingContext, shopping.ProductSearch) (shopping.ProductPage, error) {
	return shopping.ProductPage{}, nil
}

// newCheckoutTestSession primes Core's ShoppingContext through the same
// exported SelectStore/ApplyBasket/SelectTimeSlot path a real client would
// use — not by poking unexported fields — then registers only the checkout
// tools, so order_prepare/order_status are exercised the way a real MCP
// client would reach them: through a basket/store/timeslot already bound.
func newCheckoutTestSession(t *testing.T, basket shopping.Basket) *mcp.ClientSession {
	t.Helper()
	gate := checkout.NewGate()
	t.Cleanup(func() { _ = gate.Close() })
	basketGateway := stubCheckoutBasketGateway{basket: basket}
	core := shopping.NewCore(stubBasketAuthenticator{}, stubCheckoutStoresGateway{}, basketGateway, nil, gate)

	if _, err := core.SelectStore(t.Context(), basket.StoreID, "10115"); err != nil {
		t.Fatalf("prime SelectStore: %v", err)
	}
	if _, err := core.ApplyBasket(t.Context(), shopping.BasketMutation{}); err != nil {
		t.Fatalf("prime ApplyBasket: %v", err)
	}
	if _, err := core.SelectTimeSlot(t.Context(), basket.TimeSlotID); err != nil {
		t.Fatalf("prime SelectTimeSlot: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "grocery-mcp-test", Version: "0.0.0"}, nil)
	RegisterCheckoutTools(server, core)

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

func testCheckoutBasket() shopping.Basket {
	return shopping.Basket{
		ID:         "basket-1",
		StoreID:    "store-1",
		TimeSlotID: "slot-1",
		Items:      []shopping.BasketItem{{ProductID: "p1", Name: "Milk", Quantity: 1, UnitPrice: shopping.Money{Cents: 100, Currency: "EUR"}, LineTotal: shopping.Money{Cents: 100, Currency: "EUR"}}},
		Total:      shopping.Money{Cents: 100, Currency: "EUR"},
	}
}

func TestCheckoutToolsAreReachableThroughMCP(t *testing.T) {
	session := newCheckoutTestSession(t, testCheckoutBasket())
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		got = append(got, tool.Name)
	}
	if len(got) != 2 || !strings.Contains(strings.Join(got, ","), "order_prepare") || !strings.Contains(strings.Join(got, ","), "order_status") {
		t.Fatalf("tools = %v, want exactly order_prepare and order_status", got)
	}
}

func TestOrderPrepareNeverExposesACommitCapableTool(t *testing.T) {
	session := newCheckoutTestSession(t, testCheckoutBasket())
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if strings.Contains(tool.Name, "commit") || strings.Contains(tool.Name, "approve") || strings.Contains(tool.Name, "place") {
			t.Fatalf("found a commit-capable-sounding tool: %s — no MCP tool must ever be able to place an order", tool.Name)
		}
	}
}

func TestOrderPrepareReturnsPendingApprovalWithBasketBound(t *testing.T) {
	session := newCheckoutTestSession(t, testCheckoutBasket())
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "order_prepare"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("order_prepare returned an error result: %+v", result.Content)
	}
	out := structuredContent[CheckoutApproval](t, result)
	if out.Status != "pending" || out.ApprovalID == "" || out.ApprovalURL == "" {
		t.Fatalf("order_prepare output = %#v", out)
	}
	if out.Basket.ID != "basket-1" || out.StoreID != "store-1" || out.TimeSlotID != "slot-1" {
		t.Fatalf("order_prepare did not bind the current basket/store/timeslot: %#v", out)
	}
}

func TestOrderStatusReflectsHumanApprovalOnTheLocalPage(t *testing.T) {
	session := newCheckoutTestSession(t, testCheckoutBasket())
	prepared := structuredContent[CheckoutApproval](t, mustCallTool(t, session, "order_prepare", nil))

	// The MCP tool surface itself has no way to approve — only a human,
	// on the local page, can. Simulating exactly that here: load the page
	// a human would actually see, extract its action token, then submit
	// the form. ApprovalURL alone (what order_prepare's output contains)
	// is deliberately not enough — see TestOrderPrepareOutputAloneCannotApprove.
	token := reviewPageActionToken(t, prepared.ApprovalURL)
	res, err := http.PostForm(prepared.ApprovalURL, url.Values{"token": {token}})
	if err != nil {
		t.Fatalf("simulate human approval click: %v", err)
	}
	_ = res.Body.Close()

	status := structuredContent[CheckoutApproval](t, mustCallTool(t, session, "order_status", map[string]any{"approval_id": prepared.ApprovalID}))
	// The commit is deliberately stubbed (docs/spikes/checkout.md) — this
	// proves the full approval pipeline works end to end without ever
	// letting an unverified guess reach a real REWE order-placing call.
	if status.Status != "commit_failed" || status.FailureReason == "" {
		t.Fatalf("order_status after approval = %#v, want a reported (stubbed) commit failure", status)
	}
}

// TestOrderPrepareOutputAloneCannotApprove proves order_prepare's tool
// output — the only thing the calling model ever receives — is not, by
// itself, a credential sufficient to approve an order. AGENTS.md: "the
// model must never receive a token that is independently sufficient to
// authorize spending."
func TestOrderPrepareOutputAloneCannotApprove(t *testing.T) {
	session := newCheckoutTestSession(t, testCheckoutBasket())
	prepared := structuredContent[CheckoutApproval](t, mustCallTool(t, session, "order_prepare", nil))

	res, err := http.Post(prepared.ApprovalURL, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST approval_url with no token: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — approval_url alone must not be enough to approve", res.StatusCode)
	}

	status := structuredContent[CheckoutApproval](t, mustCallTool(t, session, "order_status", map[string]any{"approval_id": prepared.ApprovalID}))
	if status.Status != "pending" {
		t.Fatalf("order_status = %#v, want still pending — a token-less POST must not change approval state", status)
	}
}

func reviewPageActionToken(t *testing.T, reviewURL string) string {
	t.Helper()
	res, err := http.Get(reviewURL)
	if err != nil {
		t.Fatalf("GET review page: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read review page: %v", err)
	}
	const marker = `name="token" value="`
	idx := strings.Index(string(body), marker)
	if idx < 0 {
		t.Fatalf("review page has no action token: %s", body)
	}
	rest := string(body)[idx+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("malformed action token in review page: %s", body)
	}
	return rest[:end]
}

func TestOrderStatusUnknownApprovalIDIsAnError(t *testing.T) {
	session := newCheckoutTestSession(t, testCheckoutBasket())
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "order_status", Arguments: map[string]any{"approval_id": "does-not-exist"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("order_status for an unknown approval_id succeeded, want an error result")
	}
}

func mustCallTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned an error result: %+v", name, result.Content)
	}
	return result
}

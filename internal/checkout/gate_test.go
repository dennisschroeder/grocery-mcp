package checkout

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// extractActionToken loads the review page a human would actually see and
// pulls out its hidden form token — the only way to obtain a value that
// approve/decline will accept, since Prepare never returns it directly.
func extractActionToken(t *testing.T, reviewURL string) string {
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

func postAction(t *testing.T, actionURL, token string) *http.Response {
	t.Helper()
	res, err := http.PostForm(actionURL, url.Values{"token": {token}})
	if err != nil {
		t.Fatalf("POST %s: %v", actionURL, err)
	}
	return res
}

func testBasket() shopping.Basket {
	return shopping.Basket{
		ID:         "basket-1",
		StoreID:    "store-1",
		TimeSlotID: "slot-1",
		Items:      []shopping.BasketItem{{ProductID: "p1", Name: "Milk", Quantity: 2, UnitPrice: shopping.Money{Cents: 100, Currency: "EUR"}, LineTotal: shopping.Money{Cents: 200, Currency: "EUR"}}},
		Total:      shopping.Money{Cents: 200, Currency: "EUR"},
	}
}

func newTestGate(t *testing.T, now time.Time) *Gate {
	t.Helper()
	g := &Gate{now: func() time.Time { return now }}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func TestGatePrepareReturnsPendingApprovalBoundToSnapshot(t *testing.T) {
	g := newTestGate(t, time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC))
	basket := testBasket()
	approval, err := g.Prepare(t.Context(), basket, "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if approval.Status != shopping.ApprovalPending {
		t.Fatalf("Status = %q, want pending", approval.Status)
	}
	if approval.ID == "" || approval.ApprovalURL == "" {
		t.Fatalf("Prepare() = %#v, want non-empty ID and ApprovalURL", approval)
	}
	if !strings.HasPrefix(approval.ApprovalURL, "http://127.0.0.1:") {
		t.Fatalf("ApprovalURL = %q, want loopback-only", approval.ApprovalURL)
	}
}

func TestGateStatusUnknownIDFailsClosed(t *testing.T) {
	g := newTestGate(t, time.Now())
	_, err := g.Status(t.Context(), "does-not-exist", testBasket())
	var validationErr *shopping.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "approval_id" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGateStatusExpiresAfterTTL(t *testing.T) {
	created := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	g := newTestGate(t, created)
	approval, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	g.now = func() time.Time { return created.Add(approvalTTL + time.Second) }
	got, err := g.Status(t.Context(), approval.ID, testBasket())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != shopping.ApprovalExpired {
		t.Fatalf("Status = %q, want expired", got.Status)
	}
}

func TestGateStatusInvalidatesOnBasketChange(t *testing.T) {
	g := newTestGate(t, time.Now())
	approval, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	changed := testBasket()
	changed.Total.Cents = 999
	got, err := g.Status(t.Context(), approval.ID, changed)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != shopping.ApprovalInvalidated {
		t.Fatalf("Status = %q, want invalidated", got.Status)
	}
}

func TestGateStatusInvalidatesOnStoreChange(t *testing.T) {
	g := newTestGate(t, time.Now())
	approval, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	changed := testBasket()
	changed.StoreID = "store-2"
	got, err := g.Status(t.Context(), approval.ID, changed)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != shopping.ApprovalInvalidated {
		t.Fatalf("Status = %q, want invalidated on a store change", got.Status)
	}
}

func TestGateStatusInvalidatesOnTimeSlotChange(t *testing.T) {
	g := newTestGate(t, time.Now())
	approval, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	changed := testBasket()
	changed.TimeSlotID = "slot-2"
	got, err := g.Status(t.Context(), approval.ID, changed)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != shopping.ApprovalInvalidated {
		t.Fatalf("Status = %q, want invalidated on a timeslot change", got.Status)
	}
}

func TestGateStatusStaysPendingOnUnchangedBasket(t *testing.T) {
	g := newTestGate(t, time.Now())
	approval, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	same := testBasket()
	same.ObservedAt = time.Now() // freshness must not affect equality
	got, err := g.Status(t.Context(), approval.ID, same)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != shopping.ApprovalPending {
		t.Fatalf("Status = %q, want still pending", got.Status)
	}
}

func TestGatePrepareSupersedesPriorApproval(t *testing.T) {
	g := newTestGate(t, time.Now())
	first, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	firstToken := extractActionToken(t, first.ApprovalURL)

	second, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("second Prepare() error = %v", err)
	}
	if _, err := g.Status(t.Context(), first.ID, testBasket()); err == nil {
		t.Fatalf("Status() for superseded approval succeeded, want an error")
	}

	// Prepare restarts the approval-page server on a fresh port, so the old
	// URL is unreachable outright (connection refused) — not tested here.
	// What matters is that the old token doesn't carry over to the new
	// approval, which shares no state with the superseded one.
	staleOnNew := postAction(t, second.ApprovalURL, firstToken)
	defer staleOnNew.Body.Close()
	if staleOnNew.StatusCode != http.StatusForbidden {
		t.Fatalf("POST current approval's URL with the superseded approval's token: status = %d, want 403", staleOnNew.StatusCode)
	}
	got, err := g.Status(t.Context(), second.ID, testBasket())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != shopping.ApprovalPending {
		t.Fatalf("Status() = %#v, want still pending — a stale token must not touch the current approval's state", got)
	}
}

// --- local approval page, over real loopback HTTP ---

func TestGateApprovePageAlwaysFailsClosedOnCommit(t *testing.T) {
	g := newTestGate(t, time.Now())
	approval, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	token := extractActionToken(t, approval.ApprovalURL)

	res := postAction(t, approval.ApprovalURL, token)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), string(shopping.ApprovalCommitFailed)) {
		t.Fatalf("approve response = %q, want it to report commit_failed", body)
	}

	got, err := g.Status(t.Context(), approval.ID, testBasket())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != shopping.ApprovalCommitFailed || got.FailureReason == "" {
		t.Fatalf("Status() = %#v, want commit_failed with a reason", got)
	}
}

// TestGateApproveWithoutTokenIsRejected proves knowing ApprovalURL alone —
// e.g. because order_prepare's tool output handed it to the calling model —
// is not sufficient to approve an order. Only a value scraped from the
// actually-rendered review page works.
func TestGateApproveWithoutTokenIsRejected(t *testing.T) {
	g := newTestGate(t, time.Now())
	approval, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	res, err := http.Post(approval.ApprovalURL, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST approve without token: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a missing token", res.StatusCode)
	}

	got, err := g.Status(t.Context(), approval.ID, testBasket())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != shopping.ApprovalPending {
		t.Fatalf("Status() = %#v, want still pending — a rejected token must not touch approval state", got)
	}

	wrongToken := postAction(t, approval.ApprovalURL, "not-the-real-token")
	defer wrongToken.Body.Close()
	if wrongToken.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a wrong token", wrongToken.StatusCode)
	}
}

func TestGateDeclinePageMarksDeclined(t *testing.T) {
	g := newTestGate(t, time.Now())
	approval, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	token := extractActionToken(t, approval.ApprovalURL)
	declineURL := strings.Replace(approval.ApprovalURL, "/approve/", "/decline/", 1)

	res := postAction(t, declineURL, token)
	defer res.Body.Close()

	got, err := g.Status(t.Context(), approval.ID, testBasket())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != shopping.ApprovalDeclined {
		t.Fatalf("Status() = %#v, want declined", got)
	}
}

func TestGateApproveTwiceSecondCallDoesNotReCommit(t *testing.T) {
	g := newTestGate(t, time.Now())
	approval, err := g.Prepare(t.Context(), testBasket(), "store-1", "slot-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	token := extractActionToken(t, approval.ApprovalURL)
	first := postAction(t, approval.ApprovalURL, token)
	_ = first.Body.Close()
	res := postAction(t, approval.ApprovalURL, token)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), string(shopping.ApprovalCommitFailed)) {
		t.Fatalf("second approve response = %q, want the already-resolved status reported back, not a fresh attempt", body)
	}
}

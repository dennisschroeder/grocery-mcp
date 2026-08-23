// Package checkout implements DESIGN.md's CheckoutGate: ShoppingCore's other
// direct dependency, sibling to ReweGateway, never REWE transport itself.
// It owns human approval, the local approval page, and — once a live
// verification pass corrects the guesses in it — commit and reconciliation.
package checkout

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// approvalTTL bounds how long a human has to review an order_prepare
// snapshot before it expires unactioned. DESIGN.md: "creates a short-lived
// approval ID."
const approvalTTL = 10 * time.Minute

// Gate implements shopping.CheckoutGate. Only one approval is tracked at a
// time, matching this project's single-account, single-basket-in-flight
// shape — a new Prepare call replaces whatever was there before rather than
// accumulating an unbounded history.
type Gate struct {
	now func() time.Time

	mu sync.Mutex
	// actionToken is never exposed through shopping.CheckoutApproval or any
	// MCP tool output — only the rendered review page carries it. This is
	// what makes ApprovalURL a read-only, side-effect-free link: knowing it
	// alone (e.g. because the model itself received it as a tool result) is
	// no longer sufficient to approve/decline, closing the self-approval gap
	// flagged in card #12's review.
	actionToken string
	approval    *shopping.CheckoutApproval
	server      *http.Server
	listener    net.Listener
}

func NewGate() *Gate {
	return &Gate{now: time.Now}
}

// Close shuts down the local approval-page server, if one is running. Safe
// to call even if Prepare was never called.
func (g *Gate) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shutdownServerLocked()
}

func (g *Gate) shutdownServerLocked() error {
	if g.server == nil {
		return nil
	}
	err := g.server.Close()
	g.server, g.listener = nil, nil
	return err
}

// Prepare implements shopping.CheckoutGate. It never blocks on the human's
// decision — it mints the approval, (re)starts the local approval-page
// server if needed, and returns immediately; order_status is the only way
// to observe what the human does next.
func (g *Gate) Prepare(_ context.Context, basket shopping.Basket, storeID shopping.StoreID, timeSlotID shopping.TimeSlotID) (shopping.CheckoutApproval, error) {
	id, err := newApprovalID()
	if err != nil {
		return shopping.CheckoutApproval{}, err
	}
	token, err := newRandomToken()
	if err != nil {
		return shopping.CheckoutApproval{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// A prior approval, if any, is superseded outright — Prepare always
	// reflects the latest basket/store/timeslot snapshot, and this project
	// tracks at most one order in flight at a time.
	if err := g.shutdownServerLocked(); err != nil {
		return shopping.CheckoutApproval{}, err
	}
	if err := g.startServerLocked(); err != nil {
		return shopping.CheckoutApproval{}, err
	}

	now := g.now()
	approval := &shopping.CheckoutApproval{
		ID:          id,
		Status:      shopping.ApprovalPending,
		Basket:      basket,
		StoreID:     storeID,
		TimeSlotID:  timeSlotID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(approvalTTL),
		ApprovalURL: g.approvalURLLocked(id),
	}
	g.approval = approval
	g.actionToken = token
	return *approval, nil
}

// Status implements shopping.CheckoutGate, re-validating the approval
// against current on every call — see reviseLocked.
func (g *Gate) Status(_ context.Context, id shopping.ApprovalID, current shopping.Basket) (shopping.CheckoutApproval, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	approval, err := g.lookupLocked(id)
	if err != nil {
		return shopping.CheckoutApproval{}, err
	}
	g.reviseLocked(approval, current)
	return *approval, nil
}

func (g *Gate) lookupLocked(id shopping.ApprovalID) (*shopping.CheckoutApproval, error) {
	if g.approval == nil || g.approval.ID != id {
		return nil, &shopping.ValidationError{Operation: "order status", Field: "approval_id", Problem: shopping.ValidationInvalid}
	}
	return g.approval, nil
}

// validTokenLocked checks the token a POST /approve or /decline request
// carries against the one embedded in the rendered review page — never
// against anything an MCP tool call returns. A missing or wrong token is
// rejected without touching approval state, so a bad guess can't burn a
// real human's pending approval.
func (g *Gate) validTokenLocked(id shopping.ApprovalID, token string) bool {
	if g.approval == nil || g.approval.ID != id || token == "" || g.actionToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(g.actionToken)) == 1
}

// reviseLocked lazily transitions a still-Pending approval to Expired or
// Invalidated. Checked on every read rather than driven by a background
// timer, so no goroutine runs for the lifetime of an approval nobody is
// polling — the tradeoff is that an expired or invalidated approval's
// status only updates the next time something calls Status (from
// order_status) or hits the local page, not the instant it happens.
func (g *Gate) reviseLocked(approval *shopping.CheckoutApproval, current shopping.Basket) {
	if approval.Status != shopping.ApprovalPending {
		return
	}
	if g.now().After(approval.ExpiresAt) {
		approval.Status = shopping.ApprovalExpired
		return
	}
	if !basketsEqual(approval.Basket, current) {
		approval.Status = shopping.ApprovalInvalidated
	}
}

// basketsEqual compares content, not freshness — ObservedAt is deliberately
// excluded so re-fetching an unchanged basket never spuriously invalidates
// an approval.
func basketsEqual(a, b shopping.Basket) bool {
	if a.ID != b.ID || a.StoreID != b.StoreID || a.TimeSlotID != b.TimeSlotID {
		return false
	}
	if a.Subtotal != b.Subtotal || a.Fees != b.Fees || a.Total != b.Total {
		return false
	}
	if len(a.Items) != len(b.Items) {
		return false
	}
	for i := range a.Items {
		if a.Items[i] != b.Items[i] {
			return false
		}
	}
	return true
}

// commit is deliberately stubbed. docs/spikes/checkout.md (cards #11/#12)
// confirmed checkout creation happens server-side on www.rewe.de and is
// unobservable by any technique available in that environment; the
// payments/confirmations/orders sequence is well-evidenced from
// yannick-cw/korb's real, working implementation but only against a
// different REWE host (mobile-clients-api.rewe.de), never independently
// confirmed on this project's own origin. AGENTS.md rules out shipping a
// guess straight to an order-placing call the way SelectTimeSlot's old
// customerId guess reached a merely-reversible reservation — this is not
// reversible. Implementing this for real needs its own live-verification
// pass against www.rewe.de first. Always fails closed.
func (g *Gate) commit(_ context.Context, id shopping.ApprovalID) shopping.CheckoutApproval {
	g.mu.Lock()
	defer g.mu.Unlock()
	approval, err := g.lookupLocked(id)
	if err != nil {
		return shopping.CheckoutApproval{}
	}
	approval.Status = shopping.ApprovalCommitFailed
	approval.FailureReason = "checkout commit is not yet implemented — REWE's checkouts/payments/confirmations/orders sequence needs its own live-verification pass against www.rewe.de before any order can be placed (see docs/spikes/checkout.md)"
	return *approval
}

func newApprovalID() (shopping.ApprovalID, error) {
	token, err := newRandomToken()
	if err != nil {
		return "", fmt.Errorf("generate approval id: %w", err)
	}
	return shopping.ApprovalID(token), nil
}

func newRandomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

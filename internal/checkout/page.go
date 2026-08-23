package checkout

import (
	"fmt"
	"html/template"
	"net"
	"net/http"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// startServerLocked binds to loopback only, on an OS-assigned port — never
// a fixed one, and never any non-loopback address. The approval ID in the
// URL path is the only access control; nothing about this server is meant
// to be reachable from outside the machine.
func (g *Gate) startServerLocked() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start approval page listener: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /approve/{id}", g.handleReview)
	mux.HandleFunc("POST /approve/{id}", g.handleApprove)
	mux.HandleFunc("POST /decline/{id}", g.handleDecline)
	server := &http.Server{Handler: mux}
	g.listener, g.server = listener, server
	go server.Serve(listener) //nolint:errcheck // Serve always returns non-nil; shutdown is via Close, not this error
	return nil
}

func (g *Gate) approvalURLLocked(id shopping.ApprovalID) string {
	return fmt.Sprintf("http://%s/approve/%s", g.listener.Addr(), id)
}

func (g *Gate) handleReview(w http.ResponseWriter, r *http.Request) {
	id := shopping.ApprovalID(r.PathValue("id"))
	g.mu.Lock()
	approval, err := g.lookupLocked(id)
	if err != nil {
		g.mu.Unlock()
		http.Error(w, "unknown or expired approval", http.StatusNotFound)
		return
	}
	// Only expiry is checked here, not basket-content invalidation: this
	// page has no way to re-fetch REWE's live basket itself (the Gate is
	// deliberately REWE-agnostic — see CheckoutGate's doc comment). A
	// change since Prepare only surfaces once something calls order_status,
	// which does have a fresh basket to compare against.
	g.reviseLocked(approval, approval.Basket)
	snapshot := *approval
	token := g.actionToken
	g.mu.Unlock()
	renderReviewPage(w, snapshot, token)
}

func (g *Gate) handleApprove(w http.ResponseWriter, r *http.Request) {
	id := shopping.ApprovalID(r.PathValue("id"))
	if !g.checkToken(id, r.FormValue("token")) {
		http.Error(w, "missing or invalid approval token — open the review page and submit its form", http.StatusForbidden)
		return
	}
	stillPending, result := g.markLocked(id, shopping.ApprovalApproved)
	if !stillPending {
		renderResultPage(w, result)
		return
	}
	renderResultPage(w, g.commit(r.Context(), id))
}

func (g *Gate) handleDecline(w http.ResponseWriter, r *http.Request) {
	id := shopping.ApprovalID(r.PathValue("id"))
	if !g.checkToken(id, r.FormValue("token")) {
		http.Error(w, "missing or invalid approval token — open the review page and submit its form", http.StatusForbidden)
		return
	}
	_, result := g.markLocked(id, shopping.ApprovalDeclined)
	renderResultPage(w, result)
}

func (g *Gate) checkToken(id shopping.ApprovalID, token string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.validTokenLocked(id, token)
}

// markLocked transitions a still-Pending approval to next, first applying
// reviseLocked's lazy expiry check. Reports false (and the approval's
// actual current state) if it was no longer Pending by the time this ran.
func (g *Gate) markLocked(id shopping.ApprovalID, next shopping.ApprovalStatus) (bool, shopping.CheckoutApproval) {
	g.mu.Lock()
	defer g.mu.Unlock()
	approval, err := g.lookupLocked(id)
	if err != nil {
		return false, shopping.CheckoutApproval{}
	}
	g.reviseLocked(approval, approval.Basket)
	if approval.Status != shopping.ApprovalPending {
		return false, *approval
	}
	approval.Status = next
	return true, *approval
}

// reviewPageData carries the action token alongside the approval snapshot.
// The token only ever reaches the caller through this rendered page — never
// through shopping.CheckoutApproval or any MCP tool output — so a human
// must actually load this page before either form below can do anything.
type reviewPageData struct {
	shopping.CheckoutApproval
	ActionToken string
}

var reviewPageTemplate = template.Must(template.New("review").Parse(`<!doctype html>
<title>Approve REWE order</title>
<h1>Review your REWE order</h1>
{{if eq .Status "pending"}}
<p>Store: {{.StoreID}} &middot; Pickup slot: {{.TimeSlotID}}</p>
<table>
{{range .Basket.Items}}<tr><td>{{.Quantity}}&times; {{.Name}}</td><td>{{.LineTotal.Cents}} {{.LineTotal.Currency}}</td></tr>
{{end}}
</table>
<p>Total: {{.Basket.Total.Cents}} {{.Basket.Total.Currency}}</p>
<form method="post" action="/approve/{{.ID}}"><input type="hidden" name="token" value="{{.ActionToken}}"><button type="submit">Approve and place order</button></form>
<form method="post" action="/decline/{{.ID}}"><input type="hidden" name="token" value="{{.ActionToken}}"><button type="submit">Decline</button></form>
{{else}}
<p>This approval is no longer pending (status: {{.Status}}).</p>
{{end}}
`))

var resultPageTemplate = template.Must(template.New("result").Parse(`<!doctype html>
<title>REWE order {{.Status}}</title>
<h1>{{.Status}}</h1>
{{if .FailureReason}}<p>{{.FailureReason}}</p>{{end}}
{{if .OrderID}}<p>Order ID: {{.OrderID}}</p>{{end}}
`))

func renderReviewPage(w http.ResponseWriter, approval shopping.CheckoutApproval, actionToken string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := reviewPageData{CheckoutApproval: approval, ActionToken: actionToken}
	if err := reviewPageTemplate.Execute(w, data); err != nil {
		http.Error(w, "failed to render approval page", http.StatusInternalServerError)
	}
}

func renderResultPage(w http.ResponseWriter, approval shopping.CheckoutApproval) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := resultPageTemplate.Execute(w, approval); err != nil {
		http.Error(w, "failed to render result page", http.StatusInternalServerError)
	}
}

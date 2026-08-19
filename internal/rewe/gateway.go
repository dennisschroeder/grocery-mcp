package rewe

import (
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
)

// Gateway implements shopping.ReweGateway. Its methods are split across
// gateway_stores.go, gateway_basket.go, and gateway_orders.go — one file per
// Phase 1 vertical slice, each adding methods to this same struct so the
// verticals can be built in parallel without colliding on one file. This
// file defines only the shared struct; do not add methods here.
type Gateway struct {
	Transport browserbridge.Transport
	Now       func() time.Time
}

func (g Gateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

package rewe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// BrowserValidator proves a browser session is live by asking the browser
// bridge transport to run session_identity in the signed-in REWE tab, rather
// than building an http.Request itself — there is no cookie or header to
// attach on this side of the bridge anymore.
type BrowserValidator struct {
	Transport browserbridge.Transport
	Now       func() time.Time
}

func (v BrowserValidator) ValidateSession(ctx context.Context) (shopping.SessionIdentity, error) {
	result, err := v.Transport.Do(ctx, browserbridge.OperationSessionIdentity, nil)
	if err != nil {
		return shopping.SessionIdentity{}, classifyReadBridgeError("validate session", err)
	}

	listID, err := decodeFavoriteListID(bytes.NewReader(result))
	if err != nil {
		return shopping.SessionIdentity{}, err
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	accountHash := sha256.Sum256([]byte("rewe-favorite-list:" + listID))
	return shopping.SessionIdentity{
		AccountID:  shopping.AccountID("rewe_" + hex.EncodeToString(accountHash[:16])),
		ObservedAt: now().UTC(),
	}, nil
}

// decodeFavoriteListID parses GET /shop/api/favorites, which returns a
// top-level JSON array of favorite-list objects, each carrying its own "id"
// directly (confirmed against a live, signed-in response on 2026-08-18 —
// the wrapping {favoriteLists:{favorites:[...]}} shape this replaced was
// never observed against a real account; it was a speculative assumption
// written before any live authentication attempt had succeeded).
func decodeFavoriteListID(reader io.Reader) (string, error) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return "", &shopping.UpstreamChangeError{Operation: "validate session", Problem: shopping.UpstreamMalformedJSON}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", &shopping.UpstreamChangeError{Operation: "validate session", Problem: shopping.UpstreamTrailingPayload}
	}
	lists, ok := payload.([]any)
	if !ok {
		return "", &shopping.UpstreamChangeError{Operation: "validate session", Problem: shopping.UpstreamTypeChanged}
	}
	if len(lists) == 0 {
		return "", &shopping.UpstreamChangeError{Operation: "validate session", Problem: shopping.UpstreamIncompatiblePayload}
	}
	first, ok := lists[0].(map[string]any)
	if !ok {
		return "", &shopping.UpstreamChangeError{Operation: "validate session", Problem: shopping.UpstreamTypeChanged}
	}
	id, present := first["id"]
	if !present {
		return "", &shopping.UpstreamChangeError{Operation: "validate session", Problem: shopping.UpstreamMissingCriticalField}
	}
	idString, ok := id.(string)
	if !ok {
		return "", &shopping.UpstreamChangeError{Operation: "validate session", Problem: shopping.UpstreamTypeChanged}
	}
	idString = strings.TrimSpace(idString)
	if idString == "" {
		return "", &shopping.UpstreamChangeError{Operation: "validate session", Problem: shopping.UpstreamIncompatiblePayload}
	}
	return idString, nil
}

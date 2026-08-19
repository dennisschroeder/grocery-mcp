package rewe

import (
	"errors"
	"testing"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type criticalProduct struct {
	ID        string `json:"id"`
	Available *bool  `json:"available"`
}

func (criticalProduct) criticalFields() []string {
	return []string{"id", "available"}
}

func (p criticalProduct) validate() shopping.UpstreamProblem {
	if p.ID == "" {
		return shopping.UpstreamMissingCriticalField
	}
	if p.Available == nil {
		return shopping.UpstreamMissingCriticalField
	}
	return ""
}

func TestDecodeCriticalAcceptsCompatiblePayload(t *testing.T) {
	payload, err := decodeCritical[criticalProduct]("products.search", []byte(`{"id":"product-1","available":true,"new_field":"ignored"}`))
	if err != nil {
		t.Fatalf("decodeCritical() error = %v", err)
	}
	if payload.ID != "product-1" || payload.Available == nil || !*payload.Available {
		t.Fatalf("decodeCritical() = %#v", payload)
	}
}

func TestDecodeCriticalClassifiesIncompatiblePayloads(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		problem shopping.UpstreamProblem
	}{
		{"malformed JSON", `{"id":`, shopping.UpstreamMalformedJSON},
		{"wrong type", `{"id":42,"available":true}`, shopping.UpstreamTypeChanged},
		{"missing critical field", `{"id":"product-1"}`, shopping.UpstreamMissingCriticalField},
		{"null document", `null`, shopping.UpstreamTypeChanged},
		{"trailing after null", `null{}`, shopping.UpstreamTrailingPayload},
		{"null critical field", `{"id":"product-1","available":null}`, shopping.UpstreamTypeChanged},
		{"trailing payload", `{"id":"product-1","available":true}{}`, shopping.UpstreamTrailingPayload},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeCritical[criticalProduct]("products.search", []byte(test.body))
			var upstreamErr *shopping.UpstreamChangeError
			if !errors.As(err, &upstreamErr) {
				t.Fatalf("decodeCritical() error = %T, want *shopping.UpstreamChangeError", err)
			}
			if upstreamErr.Problem != test.problem {
				t.Fatalf("problem = %q, want %q", upstreamErr.Problem, test.problem)
			}
		})
	}
}

package contractfixture_test

import (
	"os"
	"strings"
	"testing"

	"github.com/dennisschroeder/grocery-mcp/internal/rewe/contractfixture"
)

func TestSanitizeJSONKeepsOnlyReviewedShape(t *testing.T) {
	input := []byte(`{
  "products": [{
    "id": "upstream-product-9281",
    "name": "Example Milk",
    "available": true,
    "price": {"cents": 199, "currency": "EUR", "internal": 12},
    "unreviewed": "discard me"
  }]
}`)
	schema := contractfixture.Object(map[string]contractfixture.Schema{
		"products": contractfixture.Array(contractfixture.Object(map[string]contractfixture.Schema{
			"id":        contractfixture.ReplaceWith("product-1"),
			"name":      contractfixture.ReplaceWith("Example Product"),
			"available": contractfixture.ReplaceWith(true),
			"price": contractfixture.Object(map[string]contractfixture.Schema{
				"cents":    contractfixture.ReplaceWith(199),
				"currency": contractfixture.ReplaceWith("EUR"),
			}),
		})),
	})

	got, err := contractfixture.SanitizeJSON(input, schema)
	if err != nil {
		t.Fatalf("SanitizeJSON() error = %v", err)
	}
	want, err := os.ReadFile("../testdata/product-search.sanitized.json")
	if err != nil {
		t.Fatalf("read sanitized fixture: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("SanitizeJSON() =\n%s\nwant:\n%s", got, want)
	}
	for _, forbidden := range []string{"unreviewed", "internal"} {
		if strings.Contains(string(got), forbidden) {
			t.Fatalf("sanitized fixture contains %q", forbidden)
		}
	}
}

func TestSanitizeJSONRejectsSensitiveInputFields(t *testing.T) {
	tests := []string{
		`{"products":[],"rstp":"synthetic-sensitive-value"}`,
		`{"account":{"email":"person@example.invalid"}}`,
		`{"orders":[{"addressLine1":"Example 1"}]}`,
	}
	for _, input := range tests {
		if _, err := contractfixture.SanitizeJSON([]byte(input), contractfixture.Object(map[string]contractfixture.Schema{})); err == nil {
			t.Fatalf("SanitizeJSON() accepted sensitive input %s", input)
		}
	}
}

func TestSanitizeJSONDoesNotEchoInputKeysInErrors(t *testing.T) {
	input := []byte(`{"person@example.invalid":{"clientSecret":"synthetic-sensitive-value"}}`)
	_, err := contractfixture.SanitizeJSON(input, contractfixture.Object(map[string]contractfixture.Schema{}))
	if err == nil {
		t.Fatal("SanitizeJSON() accepted sensitive nested input")
	}
	for _, forbidden := range []string{"person@example.invalid", "clientSecret", "synthetic-sensitive-value"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("SanitizeJSON() error contains input-derived text %q", forbidden)
		}
	}
}

func TestSanitizeJSONRejectsSensitiveAllowlistFields(t *testing.T) {
	tests := []string{
		"rstp",
		"authorization",
		"Set-Cookie",
		"token",
		"sessionToken",
		"customerEmail",
		"deliveryStreet",
		"addressLine1",
		"receiptData",
		"clientSecret",
	}
	for _, field := range tests {
		t.Run(field, func(t *testing.T) {
			schema := contractfixture.Object(map[string]contractfixture.Schema{
				field: contractfixture.ReplaceWith("synthetic"),
			})
			if _, err := contractfixture.SanitizeJSON([]byte(`{}`), schema); err == nil {
				t.Fatalf("SanitizeJSON() accepted sensitive field %q", field)
			}
		})
	}
}

func TestSanitizeJSONReplacesIdentifiers(t *testing.T) {
	schema := contractfixture.Object(map[string]contractfixture.Schema{
		"productId": contractfixture.ReplaceWith("product-1"),
	})
	got, err := contractfixture.SanitizeJSON([]byte(`{"productId":"upstream-1"}`), schema)
	if err != nil {
		t.Fatalf("SanitizeJSON() rejected a synthetic identifier: %v", err)
	}
	if strings.Contains(string(got), "upstream-1") {
		t.Fatal("SanitizeJSON() retained the upstream identifier")
	}
}

func TestSanitizeJSONRejectsShapeMismatch(t *testing.T) {
	schema := contractfixture.Object(map[string]contractfixture.Schema{
		"products": contractfixture.Array(contractfixture.ReplaceWith("synthetic")),
	})
	if _, err := contractfixture.SanitizeJSON([]byte(`{"products": {}}`), schema); err == nil {
		t.Fatal("SanitizeJSON() accepted an object where the schema requires an array")
	}
}

func TestSanitizeJSONRejectsScalarTypeChanges(t *testing.T) {
	schema := contractfixture.Object(map[string]contractfixture.Schema{
		"available": contractfixture.ReplaceWith(true),
	})
	if _, err := contractfixture.SanitizeJSON([]byte(`{"available":"yes"}`), schema); err == nil {
		t.Fatal("SanitizeJSON() accepted a scalar type change")
	}
}

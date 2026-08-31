package rewe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type criticalPayload interface {
	criticalFields() []string
	validate() shopping.UpstreamProblem
}

func decodeCritical[T criticalPayload](operation string, body []byte) (T, error) {
	var payload T
	if problem := preflightCriticalObject(body, payload.criticalFields()); problem != "" {
		return payload, upstreamChange(operation, problem, body)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&payload); err != nil {
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			return payload, upstreamChange(operation, shopping.UpstreamTypeChanged, body)
		}
		return payload, upstreamChange(operation, shopping.UpstreamMalformedJSON, body)
	}

	if problem := payload.validate(); problem != "" {
		return payload, upstreamChange(operation, problem, body)
	}
	return payload, nil
}

func preflightCriticalObject(body []byte, required []string) shopping.UpstreamProblem {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil {
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			return shopping.UpstreamTypeChanged
		}
		return shopping.UpstreamMalformedJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return shopping.UpstreamTrailingPayload
	}
	if object == nil {
		return shopping.UpstreamTypeChanged
	}
	for _, field := range required {
		value, exists := object[field]
		if !exists {
			return shopping.UpstreamMissingCriticalField
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return shopping.UpstreamTypeChanged
		}
	}
	return ""
}

func upstreamChange(operation string, problem shopping.UpstreamProblem, body []byte) error {
	return &shopping.UpstreamChangeError{Operation: operation, Problem: problem, Shape: DescribeJSONShape(body)}
}

// DescribeJSONShape reports object keys, array lengths, and scalar types —
// never values — so an unexpected upstream response can be diagnosed
// without ever printing account or credential data. Shared with
// cmd/grocery-mcp's bridge-smoke diagnostic, which has the same
// no-raw-payload rule.
func DescribeJSONShape(body []byte) string {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return ""
	}
	var b strings.Builder
	writeJSONShape(&b, value, 0, 9)
	return b.String()
}

func writeJSONShape(b *strings.Builder, value any, depth, maxDepth int) {
	indent := strings.Repeat("  ", depth)
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(b, "%sobject{%d keys}\n", indent, len(keys))
		if depth >= maxDepth {
			return
		}
		for _, k := range keys {
			fmt.Fprintf(b, "%s  %q:\n", indent, k)
			writeJSONShape(b, v[k], depth+2, maxDepth)
		}
	case []any:
		fmt.Fprintf(b, "%sarray[%d]\n", indent, len(v))
		if len(v) > 0 && depth < maxDepth {
			writeJSONShape(b, v[0], depth+1, maxDepth)
		}
	case string:
		fmt.Fprintf(b, "%sstring\n", indent)
	case float64:
		fmt.Fprintf(b, "%snumber\n", indent)
	case bool:
		fmt.Fprintf(b, "%sbool\n", indent)
	case nil:
		fmt.Fprintf(b, "%snull\n", indent)
	default:
		fmt.Fprintf(b, "%s%T\n", indent, v)
	}
}

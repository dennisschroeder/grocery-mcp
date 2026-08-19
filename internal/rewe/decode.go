package rewe

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type criticalPayload interface {
	criticalFields() []string
	validate() shopping.UpstreamProblem
}

func decodeCritical[T criticalPayload](operation string, body []byte) (T, error) {
	var payload T
	if problem := preflightCriticalObject(body, payload.criticalFields()); problem != "" {
		return payload, upstreamChange(operation, problem)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&payload); err != nil {
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			return payload, upstreamChange(operation, shopping.UpstreamTypeChanged)
		}
		return payload, upstreamChange(operation, shopping.UpstreamMalformedJSON)
	}

	if problem := payload.validate(); problem != "" {
		return payload, upstreamChange(operation, problem)
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

func upstreamChange(operation string, problem shopping.UpstreamProblem) error {
	return &shopping.UpstreamChangeError{Operation: operation, Problem: problem}
}

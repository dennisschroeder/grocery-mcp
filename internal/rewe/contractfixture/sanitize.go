package contractfixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxFixtureBytes = 1 << 20

type schemaKind uint8

const (
	kindReplace schemaKind = iota + 1
	kindObject
	kindArray
)

type Schema struct {
	kind        schemaKind
	replacement any
	fields      map[string]Schema
	item        *Schema
}

func ReplaceWith(value any) Schema {
	return Schema{kind: kindReplace, replacement: value}
}

func Object(fields map[string]Schema) Schema {
	return Schema{kind: kindObject, fields: fields}
}

func Array(item Schema) Schema {
	return Schema{kind: kindArray, item: &item}
}

func SanitizeJSON(input []byte, schema Schema) ([]byte, error) {
	if len(input) > maxFixtureBytes {
		return nil, fmt.Errorf("fixture exceeds %d bytes", maxFixtureBytes)
	}
	if err := validateSchema(schema); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode fixture: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("decode fixture: trailing JSON value")
	}
	if err := rejectSensitiveInput(raw); err != nil {
		return nil, err
	}

	sanitized, err := applySchema(raw, schema, "$")
	if err != nil {
		return nil, err
	}
	output, err := json.MarshalIndent(sanitized, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode fixture: %w", err)
	}
	return append(output, '\n'), nil
}

func rejectSensitiveInput(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isSensitiveField(key) {
				return fmt.Errorf("sensitive input field is not fixture-safe")
			}
			if err := rejectSensitiveInput(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectSensitiveInput(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func applySchema(value any, schema Schema, path string) (any, error) {
	switch schema.kind {
	case kindReplace:
		if !sameJSONKind(value, schema.replacement) {
			return nil, fmt.Errorf("%s: replacement changes the JSON value type", path)
		}
		return schema.replacement, nil
	case kindObject:
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: expected object", path)
		}
		result := make(map[string]any, len(schema.fields))
		for key, childSchema := range schema.fields {
			child, exists := object[key]
			if !exists {
				continue
			}
			sanitized, err := applySchema(child, childSchema, path+"."+key)
			if err != nil {
				return nil, err
			}
			result[key] = sanitized
		}
		return result, nil
	case kindArray:
		items, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: expected array", path)
		}
		result := make([]any, len(items))
		for index, item := range items {
			sanitized, err := applySchema(item, *schema.item, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
			result[index] = sanitized
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%s: invalid schema", path)
	}
}

func validateSchema(schema Schema) error {
	switch schema.kind {
	case kindReplace:
		if !isJSONScalar(schema.replacement) {
			return fmt.Errorf("replacement must be a JSON scalar")
		}
		if _, err := json.Marshal(schema.replacement); err != nil {
			return fmt.Errorf("replacement is not JSON-compatible: %w", err)
		}
		return nil
	case kindObject:
		if schema.fields == nil {
			return fmt.Errorf("object schema has no field map")
		}
		for key, child := range schema.fields {
			if isSensitiveField(key) {
				return fmt.Errorf("sensitive field %q cannot be allowlisted", key)
			}
			if err := validateSchema(child); err != nil {
				return fmt.Errorf("field %q: %w", key, err)
			}
		}
		return nil
	case kindArray:
		if schema.item == nil {
			return fmt.Errorf("array schema has no item schema")
		}
		return validateSchema(*schema.item)
	default:
		return fmt.Errorf("invalid fixture schema")
	}
}

func isSensitiveField(field string) bool {
	normalized := normalizeField(field)
	for blocked := range sensitiveFields {
		if strings.Contains(normalized, blocked) {
			return true
		}
	}
	return false
}

func isJSONScalar(value any) bool {
	switch value.(type) {
	case nil, bool, string, json.Number,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	default:
		return false
	}
}

func sameJSONKind(input, replacement any) bool {
	if input == nil || replacement == nil {
		return input == nil && replacement == nil
	}
	switch input.(type) {
	case bool:
		_, ok := replacement.(bool)
		return ok
	case string:
		_, ok := replacement.(string)
		return ok
	case json.Number:
		switch replacement.(type) {
		case json.Number,
			int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			return true
		}
	}
	return false
}

func normalizeField(field string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(field))
}

var sensitiveFields = map[string]struct{}{
	"accesstoken":    {},
	"address":        {},
	"authorization":  {},
	"cookie":         {},
	"email":          {},
	"firstname":      {},
	"housenumber":    {},
	"idtoken":        {},
	"lastname":       {},
	"password":       {},
	"phone":          {},
	"receiptcontent": {},
	"receiptdata":    {},
	"rstp":           {},
	"secret":         {},
	"street":         {},
	"token":          {},
}

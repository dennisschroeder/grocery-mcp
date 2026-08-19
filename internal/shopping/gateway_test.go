package shopping_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

func TestReweGatewayDoesNotExposeRawPayloadTypes(t *testing.T) {
	gateway := reflect.TypeOf((*shopping.ReweGateway)(nil)).Elem()
	for methodIndex := 0; methodIndex < gateway.NumMethod(); methodIndex++ {
		method := gateway.Method(methodIndex)
		for inputIndex := 0; inputIndex < method.Type.NumIn(); inputIndex++ {
			assertStableType(t, method.Name, method.Type.In(inputIndex), map[reflect.Type]bool{})
		}
		for outputIndex := 0; outputIndex < method.Type.NumOut(); outputIndex++ {
			assertStableType(t, method.Name, method.Type.Out(outputIndex), map[reflect.Type]bool{})
		}
	}
}

func assertStableType(t *testing.T, method string, typ reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	if typ == reflect.TypeOf((*error)(nil)).Elem() || typ == reflect.TypeOf((*context.Context)(nil)).Elem() {
		return
	}
	if typ == reflect.TypeOf(json.RawMessage{}) || typ.Kind() == reflect.Interface || typ.Kind() == reflect.Map {
		t.Fatalf("%s exposes raw payload type %s", method, typ)
	}
	if seen[typ] {
		return
	}
	seen[typ] = true

	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			t.Fatalf("%s exposes raw byte payload type %s", method, typ)
		}
		assertStableType(t, method, typ.Elem(), seen)
	case reflect.Struct:
		for fieldIndex := 0; fieldIndex < typ.NumField(); fieldIndex++ {
			assertStableType(t, method, typ.Field(fieldIndex).Type, seen)
		}
	}
}

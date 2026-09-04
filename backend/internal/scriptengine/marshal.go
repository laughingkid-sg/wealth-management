package scriptengine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/d5/tengo/v2"
)

// maxOutputDepth bounds how deeply nested a script's output may be. It also
// protects the output conversion from a self-referential Tengo map or array,
// which would otherwise recurse without end.
const maxOutputDepth = 64

// decodeInput parses a JSON document into a Tengo object graph. Numbers are
// decoded with json.Number so that integral values become tengo.Int (int64)
// rather than tengo.Float, preserving minor-unit money precision on round-trip.
// A nil or blank document yields an empty map.
func decodeInput(raw json.RawMessage) (tengo.Object, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return &tengo.Map{Value: map[string]tengo.Object{}}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("scriptengine: decode input json: %w", err)
	}
	return goToTengo(value)
}

// goToTengo converts a value produced by json.Decode (with UseNumber) into the
// corresponding Tengo object.
func goToTengo(value any) (tengo.Object, error) {
	switch v := value.(type) {
	case nil:
		return tengo.UndefinedValue, nil
	case bool:
		if v {
			return tengo.TrueValue, nil
		}
		return tengo.FalseValue, nil
	case string:
		return &tengo.String{Value: v}, nil
	case json.Number:
		return numberToTengo(v)
	case []any:
		items := make([]tengo.Object, len(v))
		for i, item := range v {
			converted, err := goToTengo(item)
			if err != nil {
				return nil, err
			}
			items[i] = converted
		}
		return &tengo.Array{Value: items}, nil
	case map[string]any:
		fields := make(map[string]tengo.Object, len(v))
		for key, item := range v {
			converted, err := goToTengo(item)
			if err != nil {
				return nil, err
			}
			fields[key] = converted
		}
		return &tengo.Map{Value: fields}, nil
	default:
		return nil, fmt.Errorf("scriptengine: unsupported input value %T", value)
	}
}

// numberToTengo keeps whole numbers as int64 and only falls back to float64 for
// values with a fraction/exponent or those too large for int64.
func numberToTengo(number json.Number) (tengo.Object, error) {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
			return &tengo.Int{Value: integer}, nil
		}
		// Too large for int64: fall through to float rather than failing.
	}
	float, err := number.Float64()
	if err != nil {
		return nil, fmt.Errorf("scriptengine: invalid number %q: %w", text, err)
	}
	return &tengo.Float{Value: float}, nil
}

// tengoToGo converts a Tengo object into a plain Go value suitable for
// json.Marshal. tengo.Int stays int64 so integers serialise without a decimal
// point. Unsupported object types (functions, errors, ...) are rejected rather
// than silently coerced.
func tengoToGo(object tengo.Object, depth int) (any, error) {
	if depth > maxOutputDepth {
		return nil, fmt.Errorf("scriptengine: output nested deeper than %d levels", maxOutputDepth)
	}
	switch o := object.(type) {
	case *tengo.Undefined:
		return nil, nil
	case *tengo.Int:
		return o.Value, nil
	case *tengo.Float:
		return o.Value, nil
	case *tengo.String:
		return o.Value, nil
	case *tengo.Bool:
		return o == tengo.TrueValue, nil
	case *tengo.Char:
		return string(o.Value), nil
	case *tengo.Bytes:
		return string(o.Value), nil
	case *tengo.Time:
		return o.Value.UTC().Format(time.RFC3339Nano), nil
	case *tengo.Array:
		return sliceToGo(o.Value, depth)
	case *tengo.ImmutableArray:
		return sliceToGo(o.Value, depth)
	case *tengo.Map:
		return mapToGo(o.Value, depth)
	case *tengo.ImmutableMap:
		return mapToGo(o.Value, depth)
	default:
		return nil, fmt.Errorf("scriptengine: unsupported output value type %s", object.TypeName())
	}
}

func sliceToGo(items []tengo.Object, depth int) ([]any, error) {
	result := make([]any, len(items))
	for i, item := range items {
		converted, err := tengoToGo(item, depth+1)
		if err != nil {
			return nil, err
		}
		result[i] = converted
	}
	return result, nil
}

func mapToGo(fields map[string]tengo.Object, depth int) (map[string]any, error) {
	result := make(map[string]any, len(fields))
	for key, item := range fields {
		converted, err := tengoToGo(item, depth+1)
		if err != nil {
			return nil, err
		}
		result[key] = converted
	}
	return result, nil
}

package runtime

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// JSONB may reorder schema keys and normalize number spelling. Compare the
// contract's JSON values, preserving numeric precision and array order.
func sameApprovalContract(a, b tool.Descriptor) bool {
	return sameJSONDocument(a, b)
}

func sameJSONDocument(a, b any) bool {
	decode := func(d any) (any, error) {
		data, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		err = decoder.Decode(&value)
		return value, err
	}
	left, err := decode(a)
	if err != nil {
		return false
	}
	right, err := decode(b)
	if err != nil {
		return false
	}
	return sameJSONValue(left, right)
}

func sameJSONValue(a, b any) bool {
	switch left := a.(type) {
	case json.Number:
		right, ok := b.(json.Number)
		if !ok {
			return false
		}
		x, ok := new(big.Rat).SetString(string(left))
		if !ok {
			return false
		}
		y, ok := new(big.Rat).SetString(string(right))
		return ok && x.Cmp(y) == 0
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, exists := right[key]
			if !exists || !sameJSONValue(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !sameJSONValue(left[i], right[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}

package rules

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"

	"pi-gateway/internal/piwire"
)

func object(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case *piwire.OrderedMap:
		if m == nil {
			return nil, false
		}
		out := make(map[string]any)
		for _, k := range m.Keys() {
			out[k], _ = m.Get(k)
		}
		return out, true
	}
	return nil, false
}
func objGet(v any, k string) (any, bool) {
	switch m := v.(type) {
	case map[string]any:
		x, ok := m[k]
		return x, ok
	case *piwire.OrderedMap:
		if m != nil {
			return m.Get(k)
		}
	}
	return nil, false
}
func objSet(v any, k string, x any) {
	switch m := v.(type) {
	case map[string]any:
		m[k] = x
	case *piwire.OrderedMap:
		m.Set(k, x)
	}
}
func objDelete(v any, k string) {
	switch m := v.(type) {
	case map[string]any:
		delete(m, k)
	case *piwire.OrderedMap:
		m.Delete(k)
	}
}
func keys(v any) []string {
	if o, ok := v.(*piwire.OrderedMap); ok {
		return o.Keys()
	}
	if m, ok := v.(map[string]any); ok {
		ks := make([]string, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	return nil
}

// cloneJSON preserves OrderedMap order and never retains mutable references.
func cloneJSON(v any) (any, error) { nodes := 0; return cloneAt(v, 0, &nodes) }
func cloneAt(v any, depth int, nodes *int) (any, error) {
	*nodes++
	if depth > MaxDepth {
		return nil, fmt.Errorf("JSON nesting exceeds %d", MaxDepth)
	}
	if *nodes > MaxPayloadBytes {
		return nil, fmt.Errorf("JSON node budget exceeded")
	}
	switch x := v.(type) {
	case nil, string, bool:
		return x, nil
	case json.Number:
		f, e := strconv.ParseFloat(x.String(), 64)
		if e != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("non-finite JSON number")
		}
		return x, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("non-finite JSON number")
		}
		return x, nil
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, fmt.Errorf("non-finite JSON number")
		}
		return float64(x), nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return x, nil
	case *piwire.OrderedMap:
		if x == nil {
			return nil, nil
		}
		out := piwire.NewOrderedMap()
		for _, k := range x.Keys() {
			v, _ := x.Get(k)
			c, e := cloneAt(v, depth+1, nodes)
			if e != nil {
				return nil, e
			}
			out.Set(k, c)
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			c, e := cloneAt(v, depth+1, nodes)
			if e != nil {
				return nil, e
			}
			out[k] = c
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			c, e := cloneAt(v, depth+1, nodes)
			if e != nil {
				return nil, e
			}
			out[i] = c
		}
		return out, nil
	case []string:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = v
		}
		return out, nil
	case Condition:
		b, e := json.Marshal(x)
		if e != nil {
			return nil, e
		}
		return decodeAny(b, "")
	case []Condition:
		out := make([]any, len(x))
		for i, c := range x {
			v, e := cloneAt(c, depth+1, nodes)
			if e != nil {
				return nil, e
			}
			out[i] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported JSON type %T", v)
}
func mustClone(v any) any {
	x, e := cloneJSON(v)
	if e != nil {
		panic(e)
	}
	return x
}
func jsonEqual(a, b any) bool {
	if na, ok := number(a); ok {
		nb, bok := number(b)
		return bok && na == nb
	}
	am, aok := object(a)
	bm, bok := object(b)
	if aok || bok {
		if !aok || !bok || len(am) != len(bm) {
			return false
		}
		for k, av := range am {
			bv, ok := bm[k]
			if !ok || !jsonEqual(av, bv) {
				return false
			}
		}
		return true
	}
	aa, aok := a.([]any)
	ba, bok := b.([]any)
	if aok || bok {
		if !aok || !bok || len(aa) != len(ba) {
			return false
		}
		for i := range aa {
			if !jsonEqual(aa[i], ba[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, e := x.Float64()
		return f, e == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case float32:
		return float64(x), !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0)
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case int16:
		return float64(x), true
	case int8:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint64:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint8:
		return float64(x), true
	}
	return 0, false
}
func jsonType(v any) string {
	if v == nil {
		return "null"
	}
	if _, ok := number(v); ok {
		return "number"
	}
	switch v.(type) {
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	}
	if _, ok := object(v); ok {
		return "object"
	}
	return "invalid"
}
func arrayIndex(s string, length int, appendOK bool) (int, error) {
	if s == "" || s == "-" || (len(s) > 1 && s[0] == '0') {
		return 0, fmt.Errorf("invalid array index %q", s)
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid array index %q", s)
		}
	}
	n, e := strconv.ParseUint(s, 10, 63)
	if e != nil || n > uint64(length) || (n == uint64(length) && !appendOK) {
		return 0, fmt.Errorf("array index %q out of bounds", s)
	}
	return int(n), nil
}
func pointerGet(root any, path string) (any, bool, error) {
	parts, e := pointerSegments(path)
	if e != nil {
		return nil, false, e
	}
	v := root
	for _, p := range parts {
		if _, ok := object(v); ok {
			var found bool
			v, found = objGet(v, p)
			if !found {
				return nil, false, nil
			}
			continue
		}
		if a, ok := v.([]any); ok {
			i, e := arrayIndex(p, len(a), false)
			if e != nil {
				if isCanonicalIndex(p) {
					return nil, false, nil
				}
				return nil, false, e
			}
			v = a[i]
			continue
		}
		return nil, false, nil
	}
	return v, true, nil
}
func isCanonicalIndex(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func pointerSet(root any, path string, value any, parents, keep bool) (any, error) {
	parts, e := pointerSegments(path)
	if e != nil {
		return root, e
	}
	if len(parts) == 0 {
		if keep {
			return root, nil
		}
		return value, nil
	}
	return setParts(root, parts, value, parents, keep)
}
func setParts(node any, parts []string, value any, parents, keep bool) (any, error) {
	p := parts[0]
	last := len(parts) == 1
	if _, ok := object(node); ok {
		old, exists := objGet(node, p)
		if last {
			if !exists || !keep {
				objSet(node, p, value)
			}
			return node, nil
		}
		if !exists {
			if !parents {
				return nil, fmt.Errorf("parent %q does not exist", p)
			}
			old = map[string]any{}
		}
		next, e := setParts(old, parts[1:], value, parents, keep)
		if e != nil {
			return nil, e
		}
		objSet(node, p, next)
		return node, nil
	}
	if arr, ok := node.([]any); ok {
		i, e := arrayIndex(p, len(arr), true)
		if e != nil {
			return nil, e
		}
		if last {
			if i == len(arr) {
				return append(arr, value), nil
			}
			if !keep {
				arr[i] = value
			}
			return arr, nil
		}
		if i == len(arr) {
			if !parents {
				return nil, fmt.Errorf("array parent missing")
			}
			arr = append(arr, map[string]any{})
		}
		next, e := setParts(arr[i], parts[1:], value, parents, keep)
		if e != nil {
			return nil, e
		}
		arr[i] = next
		return arr, nil
	}
	return nil, fmt.Errorf("parent is %s, not an object or array", jsonType(node))
}
func pointerRemove(root any, path string, missingError bool) (any, error) {
	parts, e := pointerSegments(path)
	if e != nil {
		return root, e
	}
	if len(parts) == 0 {
		return root, fmt.Errorf("removing the root is forbidden")
	}
	out, _, e := removeParts(root, parts, missingError)
	return out, e
}
func removeParts(node any, parts []string, missingError bool) (any, bool, error) {
	p := parts[0]
	last := len(parts) == 1
	if _, ok := object(node); ok {
		v, exists := objGet(node, p)
		if !exists {
			if missingError {
				return node, false, fmt.Errorf("path does not exist")
			}
			return node, false, nil
		}
		if last {
			objDelete(node, p)
			return node, true, nil
		}
		next, removed, e := removeParts(v, parts[1:], missingError)
		if e == nil && removed {
			objSet(node, p, next)
		}
		return node, removed, e
	}
	if a, ok := node.([]any); ok {
		i, e := arrayIndex(p, len(a), false)
		if e != nil {
			if missingError || !isCanonicalIndex(p) {
				return node, false, e
			}
			return node, false, nil
		}
		if last {
			return append(a[:i], a[i+1:]...), true, nil
		}
		next, removed, e := removeParts(a[i], parts[1:], missingError)
		if e == nil && removed {
			a[i] = next
		}
		return a, removed, e
	}
	if missingError {
		return node, false, fmt.Errorf("path does not exist")
	}
	return node, false, nil
}
func ensurePayload(v any) error {
	if err := checkPayloadDepth(v, 0); err != nil {
		return err
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > MaxPayloadBytes {
		return fmt.Errorf("payload exceeds %d bytes", MaxPayloadBytes)
	}
	return nil
}
func checkPayloadDepth(v any, depth int) error {
	if depth > MaxDepth {
		return fmt.Errorf("payload nesting exceeds %d", MaxDepth)
	}
	switch m := v.(type) {
	case map[string]any:
		for _, x := range m {
			if e := checkPayloadDepth(x, depth+1); e != nil {
				return e
			}
		}
	case *piwire.OrderedMap:
		for _, k := range m.Keys() {
			x, _ := m.Get(k)
			if e := checkPayloadDepth(x, depth+1); e != nil {
				return e
			}
		}
	case []any:
		for _, x := range m {
			if e := checkPayloadDepth(x, depth+1); e != nil {
				return e
			}
		}
	}
	return nil
}

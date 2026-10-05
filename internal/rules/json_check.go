package rules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Walk tokens before decoding to reject duplicates, unsafe numbers and excessive depth
// before encoding/json can silently discard or round user input.
func checkJSON(raw []byte, path string) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if e := checkToken(d, path, 0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return invalid(path, "trailing or invalid JSON at byte %d", d.InputOffset())
	}
	return nil
}
func checkToken(d *json.Decoder, path string, depth int) error {
	if depth > MaxDepth*3+16 {
		return invalid(path, "JSON nesting budget exceeded")
	}
	t, e := d.Token()
	if e != nil {
		return invalid(path, "invalid JSON at byte %d: %v", d.InputOffset(), e)
	}
	if n, ok := t.(json.Number); ok {
		_, e = normalizeNumber(n.String(), path)
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return invalid(path, "invalid object key: %v", e)
			}
			key, ok := k.(string)
			if !ok {
				return invalid(path, "expected an object key")
			}
			p := joinPointer(path, key)
			if seen[key] {
				return invalid(p, "duplicate object key")
			}
			seen[key] = true
			if e = checkToken(d, p, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for i := 0; d.More(); i++ {
			if e := checkToken(d, fmt.Sprintf("%s/%d", path, i), depth+1); e != nil {
				return e
			}
		}
	default:
		return invalid(path, "unexpected closing delimiter")
	}
	_, e = d.Token()
	if e != nil {
		return invalid(path, "invalid closing delimiter: %v", e)
	}
	return nil
}

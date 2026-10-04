package store

import "encoding/json"

// jsonMarshal and jsonUnmarshal are thin wrappers so callers do not import encoding/json
// in several places at once.

func jsonMarshal(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func jsonUnmarshal(raw string, dst any) error {
	return json.Unmarshal([]byte(raw), dst)
}

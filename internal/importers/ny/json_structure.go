package ny

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const maxJSONNesting = 256

var (
	errDuplicateJSONField = errors.New("duplicate JSON object field")
	errJSONNestingLimit   = errors.New("JSON nesting limit exceeded")
)

// A duplicate field makes source interpretation depend on parser behavior.
// The error never includes an untrusted field name or value.
func validateUniqueJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := readUniqueJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func readUniqueJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONNesting {
		return errJSONNestingLimit
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object field")
			}
			if _, exists := seen[key]; exists {
				return errDuplicateJSONField
			}
			seen[key] = struct{}{}
			if err := readUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := readUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

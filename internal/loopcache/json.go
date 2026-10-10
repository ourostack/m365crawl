package loopcache

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"
)

func unsupported() error { return &ReadError{Code: "loop_cache_snapshot_unsupported"} }
func tooLarge() error    { return &ReadError{Code: "loop_cache_snapshot_too_large"} }

type jsonBudget struct {
	ctx      context.Context
	members  int
	maxDepth int
	maxItems int
}

func (b *jsonBudget) parse(raw []byte) (any, error) {
	if !utf8.Valid(raw) || !validEscapes(raw) {
		return nil, unsupported()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := b.value(decoder, 1)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, unsupported()
	}
	return value, nil
}

func (b *jsonBudget) value(d *json.Decoder, depth int) (any, error) {
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > b.maxDepth {
		return nil, tooLarge()
	}
	token, err := d.Token()
	if err != nil {
		return nil, unsupported()
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	object := map[string]any{}
	array := []any{}
	for d.More() {
		b.members++
		if b.members > b.maxItems {
			return nil, tooLarge()
		}
		key := ""
		if delim == '{' {
			t, err := d.Token()
			if err != nil {
				return nil, unsupported()
			}
			key = t.(string)
			if _, exists := object[key]; exists {
				return nil, unsupported()
			}
		}
		value, err := b.value(d, depth+1)
		if err != nil {
			return nil, err
		}
		if delim == '{' {
			object[key] = value
		} else {
			array = append(array, value)
		}
	}
	close, err := d.Token()
	if err != nil || (delim == '{' && close != json.Delim('}')) || (delim == '[' && close != json.Delim(']')) {
		return nil, unsupported()
	}
	if delim == '{' {
		return object, nil
	}
	return array, nil
}

func validEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		code, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil || (code >= 0xdc00 && code <= 0xdfff) {
			return false
		}
		i += 4
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

func integer(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	for _, char := range number.String() {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(number.String(), 10, 64)
	return n, err == nil
}

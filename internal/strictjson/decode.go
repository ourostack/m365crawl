package strictjson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

var (
	ErrInvalid  = errors.New("strict JSON input invalid")
	ErrTooLarge = errors.New("strict JSON input too large")
)

type Budget struct {
	Context              context.Context
	MaxDepth, MaxMembers int
	members              int
}

func (b *Budget) Parse(raw []byte) (value any, err error) {
	if b == nil || b.Context == nil {
		return nil, ErrInvalid
	}
	defer func() {
		if contextErr := b.Context.Err(); contextErr != nil {
			value, err = nil, contextErr
		}
	}()
	if !utf8.Valid(raw) || !validEscapes(raw) {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err = b.value(decoder, 1)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return value, nil
}

func (b *Budget) value(d *json.Decoder, depth int) (any, error) {
	if err := b.Context.Err(); err != nil {
		return nil, err
	}
	if depth > b.MaxDepth {
		return nil, ErrTooLarge
	}
	token, err := d.Token()
	if err != nil {
		return nil, ErrInvalid
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	object := map[string]any{}
	array := []any{}
	for d.More() {
		b.members++
		if b.members > b.MaxMembers {
			return nil, ErrTooLarge
		}
		key := ""
		if delim == '{' {
			t, err := d.Token()
			if err != nil {
				return nil, ErrInvalid
			}
			key = t.(string)
			if _, exists := object[key]; exists {
				return nil, ErrInvalid
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
		return nil, ErrInvalid
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

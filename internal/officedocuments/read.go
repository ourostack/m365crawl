package officedocuments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strconv"
	"unicode/utf8"
)

type readLimits struct {
	inputBytes, fieldBytes, stringBytes int64
	rows, depth, members                int
}

type ReadError struct{ Code string }

func (e *ReadError) Error() string { return "office: " + e.Code }

func ReadJSON(ctx context.Context, source io.Reader, surface Surface) (Result, error) {
	return readJSON(ctx, source, surface, readLimits{
		inputBytes: 8 << 20, fieldBytes: 256 << 10, stringBytes: 4 << 20,
		rows: 1 << 16, depth: 64, members: 1 << 17,
	})
}

func readJSON(ctx context.Context, source io.Reader, surface Surface, limits readLimits) (result Result, err error) {
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			result, err = Result{}, contextErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	switch surface {
	case Recent, Shared, Recommended, Dialog:
	default:
		return Result{}, &ReadError{Code: "office_surface_unknown"}
	}
	if source == nil {
		return Result{}, &ReadError{Code: "office_json_unreadable"}
	}
	data, err := io.ReadAll(io.LimitReader(contextInput{ctx, source}, limits.inputBytes+1))
	if err != nil {
		return Result{}, &ReadError{Code: "office_json_unreadable"}
	}
	if int64(len(data)) > limits.inputBytes {
		return Result{}, &ReadError{Code: "office_json_too_large"}
	}
	if !utf8.Valid(data) || !validSurrogates(data) {
		return Result{}, &ReadError{Code: "office_json_unrecognized"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	members := 0
	value, err := parseValue(decoder, 0, &members, limits)
	if err != nil {
		return Result{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Result{}, &ReadError{Code: "office_json_unrecognized"}
	}
	if surface != Dialog {
		root, ok := value.(map[string]any)
		if !ok {
			return Result{}, &ReadError{Code: "office_json_unrecognized"}
		}
		switch surface {
		case Recent:
			inner, _ := root["documents"].(map[string]any)
			value = inner["items"]
		case Shared:
			value = root["shared_documents"]
		case Recommended:
			inner, _ := root["documents_group"].(map[string]any)
			value = inner["documents"]
		}
	}
	rows, ok := value.([]any)
	if !ok {
		return Result{}, &ReadError{Code: "office_json_unrecognized"}
	}
	if len(rows) > limits.rows {
		return Result{}, &ReadError{Code: "office_json_too_large"}
	}
	losses := map[string]int{}
	var retained int64
	for index, row := range rows {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		document, unknownTimes, ok := mapDocument(row, surface)
		if !ok {
			losses["office_document_unmapped"]++
			continue
		}
		for _, value := range documentStrings(document) {
			if int64(len(value)) > limits.fieldBytes {
				return Result{}, &ReadError{Code: "office_json_too_large"}
			}
			retained += int64(len(value))
			if retained > limits.stringBytes {
				return Result{}, &ReadError{Code: "office_json_too_large"}
			}
		}
		if unknownTimes > 0 {
			losses["office_timestamp_unknown"] += unknownTimes
		}
		document.Ordinal = index
		result.Documents = append(result.Documents, document)
	}
	for _, code := range slices.Sorted(maps.Keys(losses)) {
		result.Losses = append(result.Losses, Loss{Code: code, Count: losses[code]})
	}
	return result, nil
}

type contextInput struct {
	ctx    context.Context
	source io.Reader
}

func (r contextInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.source.Read(p)
	if contextErr := r.ctx.Err(); contextErr != nil {
		return n, contextErr
	}
	return n, err
}

func parseValue(decoder *json.Decoder, depth int, members *int, limits readLimits) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, &ReadError{Code: "office_json_unrecognized"}
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	if depth >= limits.depth {
		return nil, &ReadError{Code: "office_json_too_large"}
	}
	object := map[string]any{}
	var array []any
	for decoder.More() {
		*members++
		if *members > limits.members {
			return nil, &ReadError{Code: "office_json_too_large"}
		}
		var key string
		if delimiter == '{' {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, &ReadError{Code: "office_json_unrecognized"}
			}
			key, _ = keyToken.(string)
			if _, duplicate := object[key]; duplicate {
				return nil, &ReadError{Code: "office_json_unrecognized"}
			}
		}
		value, err := parseValue(decoder, depth+1, members, limits)
		if err != nil {
			return nil, err
		}
		if delimiter == '{' {
			object[key] = value
		} else {
			array = append(array, value)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, &ReadError{Code: "office_json_unrecognized"}
	}
	if delimiter == '{' {
		return object, nil
	}
	if array == nil {
		array = []any{}
	}
	return array, nil
}

func validSurrogates(data []byte) bool {
	inString := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inString = !inString
		}
		if !inString || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		code, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil || (code >= 0xdc00 && code <= 0xdfff) {
			return false
		}
		i += 4
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

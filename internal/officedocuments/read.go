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
		if value == nil && emptyServiceResponse(root, surface) {
			return Result{}, nil
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

// emptyServiceResponse recognizes a successful Office response whose document
// container is absent. Office omits the container when it lists no documents;
// a present but malformed container, an error payload or an unmarked object
// stays unrecognized.
func emptyServiceResponse(root map[string]any, surface Surface) bool {
	if !containerAbsent(root, surface) {
		return false
	}
	if errorData, present := root["error_data"]; present {
		if details, ok := errorData.(map[string]any); !ok || len(details) != 0 {
			return false
		}
	}
	marked := false
	if code, present := root["response_code"]; present {
		if code != json.Number("0") && code != json.Number("1") {
			return false
		}
		marked = true
	}
	if diagnostics, present := root["diagnostics_data"]; present {
		details, ok := diagnostics.(map[string]any)
		if !ok {
			return false
		}
		if status, present := details["http_status_code"]; present {
			if status != json.Number("200") {
				return false
			}
			marked = true
		}
	}
	return marked
}

func containerAbsent(root map[string]any, surface Surface) bool {
	path := containerPaths[surface]
	current := root
	for _, key := range path[:len(path)-1] {
		value, present := current[key]
		if !present {
			return true
		}
		next, ok := value.(map[string]any)
		if !ok {
			return false
		}
		current = next
	}
	_, present := current[path[len(path)-1]]
	return !present
}

var containerPaths = map[Surface][]string{
	Recent:      {"documents", "items"},
	Shared:      {"shared_documents"},
	Recommended: {"documents_group", "documents"},
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

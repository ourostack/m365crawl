package botcitations

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"unicode/utf8"
)

type limits struct {
	citations, fieldBytes, stringBytes int
}

func MapMessage(ctx context.Context, messageID, senderID string, message map[string]any) (Result, error) {
	return mapMessage(ctx, messageID, senderID, message, limits{4096, 256 << 10, 4 << 20})
}

func mapMessage(ctx context.Context, messageID, senderID string, message map[string]any, cap limits) (result Result, err error) {
	losses := map[string]int{}
	defer func() {
		if err == nil {
			for code, count := range losses {
				result.Losses = append(result.Losses, Loss{Code: code, Count: count})
			}
			sort.Slice(result.Losses, func(i, j int) bool { return result.Losses[i].Code < result.Losses[j].Code })
		}
		if ctx != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				err = contextErr
			}
		}
		if err != nil {
			result = Result{}
		}
	}()
	if ctx == nil {
		return Result{}, &MapError{Code: "teams_bot_input_unsupported"}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if message == nil || messageID == "" || senderID == "" || !utf8.ValidString(messageID) || !utf8.ValidString(senderID) {
		return Result{}, &MapError{Code: "teams_bot_input_unsupported"}
	}
	preflight := func(value any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if text, ok := value.(string); ok && len(text) > cap.fieldBytes {
			return &MapError{Code: "teams_bot_input_too_large"}
		}
		return nil
	}
	retained := 0
	charge := func(text string) error {
		if len(text) > cap.stringBytes-retained {
			return &MapError{Code: "teams_bot_input_too_large"}
		}
		retained += len(text)
		return nil
	}
	for _, identity := range []string{messageID, senderID} {
		if err := preflight(identity); err != nil {
			return Result{}, err
		}
		if err := charge(identity); err != nil {
			return Result{}, err
		}
	}
	result.Observation = Observation{MessageID: messageID, SenderID: senderID}
	rawProps := message["properties"]
	if isNull(rawProps) {
		return result, nil
	}
	props, ok := rawProps.(map[string]any)
	if !ok {
		losses["teams_bot_properties_unmapped"]++
		return result, nil
	}
	for _, selected := range []struct {
		container string
		fields    []string
	}{
		{"fromAppMetadata", []string{"id", "name"}},
		{"botMetadata", []string{"replyToId"}},
	} {
		object, _ := props[selected.container].(map[string]any)
		for _, field := range selected.fields {
			if err := preflight(object[field]); err != nil {
				return Result{}, err
			}
		}
	}
	metadata := func(value any, keys ...string) []*string {
		out := make([]*string, len(keys))
		if isNull(value) {
			return out
		}
		object, ok := value.(map[string]any)
		if !ok {
			losses["teams_bot_metadata_unmapped"]++
			return out
		}
		for i, key := range keys {
			str, valid := optionalString(object[key])
			if !valid {
				losses["teams_bot_metadata_field_unmapped"]++
			} else {
				out[i] = str
			}
		}
		return out
	}
	app := metadata(props["fromAppMetadata"], "id", "name")
	bot := metadata(props["botMetadata"], "replyToId")
	result.Observation.AppID, result.Observation.AppName, result.Observation.ReplyToID = app[0], app[1], bot[0]
	for _, value := range []*string{app[0], app[1], bot[0]} {
		if value != nil {
			if err := charge(*value); err != nil {
				return Result{}, err
			}
		}
	}
	rawCitations := props["botCitations"]
	if isNull(rawCitations) {
		return result, nil
	}
	citations, ok := rawCitations.([]any)
	if !ok {
		losses["teams_bot_citations_unmapped"]++
		return result, nil
	}
	if len(citations) > cap.citations {
		return Result{}, &MapError{Code: "teams_bot_input_too_large"}
	}
	for position, raw := range citations {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		object, ok := raw.(map[string]any)
		if !ok || object == nil {
			losses["teams_bot_citation_unmapped"]++
			continue
		}
		for _, key := range []string{"id", "title", "link", "contentType", "content"} {
			if err := preflight(object[key]); err != nil {
				return Result{}, err
			}
		}
		id, validID := nativeInteger(object["id"])
		title, validTitle := object["title"].(string)
		link, validLink := optionalString(object["link"])
		contentType, validType := optionalString(object["contentType"])
		content := object["content"]
		present := !isNull(content)
		_, validContent := content.(map[string]any)
		if !validID || !validTitle || !utf8.ValidString(title) || !validLink || !validType || (present && !validContent) {
			losses["teams_bot_citation_unmapped"]++
			continue
		}
		if err := charge(title); err != nil {
			return Result{}, err
		}
		for _, value := range []*string{link, contentType} {
			if value != nil {
				if err := charge(*value); err != nil {
					return Result{}, err
				}
			}
		}
		result.Observation.Citations = append(result.Observation.Citations, Citation{
			Position: position, ID: id, Title: title, Link: link, ContentType: contentType, NestedContentPresent: present,
		})
		if present {
			losses["teams_bot_citation_content_unmapped"]++
		}
	}
	return result, nil
}

func isNull(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case map[string]any:
		return value == nil
	case []any:
		return value == nil
	default:
		return false
	}
}

func optionalString(value any) (*string, bool) {
	if isNull(value) {
		return nil, true
	}
	text, ok := value.(string)
	if !ok || !utf8.ValidString(text) {
		return nil, false
	}
	return &text, true
}

func nativeInteger(value any) (int64, bool) {
	switch value := value.(type) {
	case int64:
		return value, value >= 0
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 9007199254740991 || math.Trunc(value) != value {
			return 0, false
		}
		return int64(value), true
	case json.Number:
		raw := value.String()
		if raw == "" || (len(raw) > 1 && raw[0] == '0') {
			return 0, false
		}
		for _, char := range raw {
			if char < '0' || char > '9' {
				return 0, false
			}
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

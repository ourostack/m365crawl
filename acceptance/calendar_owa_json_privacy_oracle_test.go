//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type owaPrivacyMember struct {
	key        string
	raw        json.RawMessage
	start, end int
}

type owaPrivacyWalker struct {
	ctx   context.Context
	nodes int
	bytes int
}

func owaPrivacySecret(name string) bool {
	switch strings.ToLower(name) {
	case "access_token", "refresh_token", "id_token", "authorization", "client_secret", "password":
		return true
	}
	return false
}

func owaOracleStructuredPrivacy(ctx context.Context, source string, depth int) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	trimmed := strings.TrimSpace(source)
	if depth >= 4 || len(source) > owaMaxPayloadBytes || !utf8.ValidString(source) || len(trimmed) == 0 ||
		(trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid([]byte(trimmed)) {
		return "", 0, errors.New("calendar_oracle_structured_privacy_unproved")
	}
	walker := owaPrivacyWalker{ctx: ctx}
	clean, count, err := walker.value([]byte(trimmed), depth+1, 0)
	if err != nil {
		return "", 0, err
	}
	if count == 0 {
		return source, 0, nil
	}
	// Counters measure redacted values: an enclosing JSON string applies its scalar
	// rules after the inner structure is scrubbed, and a value already redacted
	// there is not counted again (#291).
	enclosed, outerCount, err := owaOracleScalarPrivacyRules(ctx, string(clean))
	return enclosed, count + outerCount, err
}

func (walker *owaPrivacyWalker) value(raw []byte, stringDepth, structureDepth int) ([]byte, int, error) {
	if err := walker.ctx.Err(); err != nil {
		return nil, 0, err
	}
	walker.nodes++
	if structureDepth > 128 || walker.nodes > owaMaxQueryRows || len(raw) == 0 ||
		len(raw) > owaMaxPayloadBytes-walker.bytes {
		return nil, 0, errors.New("calendar_oracle_json_budget_refused")
	}
	walker.bytes += len(raw)
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return nil, 0, errors.New("calendar_oracle_json_decode_refused")
		}
		clean, count, err := owaOraclePrivacyDepth(walker.ctx, text, stringDepth)
		if err != nil || count == 0 {
			return raw, count, err
		}
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if encoder.Encode(clean) != nil {
			return nil, 0, errors.New("calendar_oracle_json_encode_refused")
		}
		return bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'}), count, nil
	}
	if raw[0] != '{' && raw[0] != '[' {
		return raw, 0, nil
	}
	object := raw[0] == '{'
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return nil, 0, errors.New("calendar_oracle_json_decode_refused")
	}
	var members []owaPrivacyMember
	named := false
	for decoder.More() {
		if err := walker.ctx.Err(); err != nil {
			return nil, 0, err
		}
		member := owaPrivacyMember{}
		if object {
			key, err := decoder.Token()
			if err != nil {
				return nil, 0, errors.New("calendar_oracle_json_decode_refused")
			}
			name, ok := key.(string)
			if !ok {
				return nil, 0, errors.New("calendar_oracle_json_decode_refused")
			}
			clean, n, err := owaOraclePrivacyDepth(walker.ctx, name, stringDepth)
			if err != nil || n != 0 || clean != name {
				return nil, 0, errors.New("calendar_oracle_json_key_privacy_unproved")
			}
			member.key = name
		}
		if decoder.Decode(&member.raw) != nil {
			return nil, 0, errors.New("calendar_oracle_json_decode_refused")
		}
		offset := decoder.InputOffset()
		if offset < int64(len(member.raw)) || offset > int64(len(raw)) {
			return nil, 0, errors.New("calendar_oracle_json_span_refused")
		}
		member.end, member.start = int(offset), int(offset)-len(member.raw)
		if object {
			switch strings.ToLower(member.key) {
			case "name", "type", "key":
				var value string
				if json.Unmarshal(member.raw, &value) == nil && owaPrivacySecret(value) {
					named = true
				}
			}
		}
		members = append(members, member)
		if len(members) > owaMaxQueryRows {
			return nil, 0, errors.New("calendar_oracle_json_budget_refused")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, 0, errors.New("calendar_oracle_json_decode_refused")
	}
	pair := false
	if !object && len(members) > 1 {
		var key string
		pair = json.Unmarshal(members[0].raw, &key) == nil && owaPrivacySecret(key)
	}
	var output bytes.Buffer
	cursor, count := 0, 0
	for index, member := range members {
		if member.start < cursor || member.end > len(raw) {
			return nil, 0, errors.New("calendar_oracle_json_span_refused")
		}
		output.Write(raw[cursor:member.start])
		secret := object && (owaPrivacySecret(member.key) || named && strings.EqualFold(member.key, "value")) || pair && index == 1
		if secret {
			output.WriteString(`"[redacted]"`)
			count++
		} else {
			clean, n, err := walker.value(member.raw, stringDepth, structureDepth+1)
			if err != nil {
				return nil, 0, err
			}
			output.Write(clean)
			count += n
		}
		cursor = member.end
	}
	output.Write(raw[cursor:])
	if err := walker.ctx.Err(); err != nil {
		return nil, 0, err
	}
	return output.Bytes(), count, nil
}

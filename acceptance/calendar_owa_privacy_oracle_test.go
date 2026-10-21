//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	owaOracleJWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	owaOracleSig = regexp.MustCompile(`(?i)[?&]sig=[^&"\\\s#]+`)
)

func owaOraclePrivacyText(ctx context.Context, source string) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if !utf8.ValidString(source) || len(source) > owaMaxPayloadBytes {
		return "", 0, errors.New("calendar_oracle_text_budget_or_encoding_refused")
	}
	trimmed, lower := strings.TrimSpace(source), strings.ToLower(source)
	array := strings.TrimSpace(strings.TrimPrefix(trimmed, "["))
	structuredArray := strings.HasPrefix(trimmed, "[") && (array == "" || strings.ContainsRune(`"{[tfnu-0123456789]`, rune(array[0])))
	if strings.HasPrefix(trimmed, "{") || structuredArray ||
		strings.Contains(lower, "&amp;sig=") || strings.Contains(lower, `\u0026sig=`) {
		return "", 0, errors.New("calendar_oracle_structured_privacy_unproved")
	}
	if len(source) > 6 && strings.EqualFold(source[:6], "Bearer") {
		next, _ := utf8.DecodeRuneInString(source[6:])
		switch next {
		case ' ', '\t', '\n', '\r':
			return "[redacted]", 1, nil
		default:
			if unicode.IsSpace(next) {
				return "", 0, errors.New("calendar_oracle_bearer_whitespace_unproved")
			}
		}
	}
	count := 0
	clean := owaOracleJWT.ReplaceAllStringFunc(source, func(string) string {
		count++
		return "[redacted]"
	})
	clean = owaOracleSig.ReplaceAllStringFunc(clean, func(match string) string {
		count++
		return match[:strings.IndexByte(match, '=')+1] + "[redacted]"
	})
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	return clean, count, nil
}

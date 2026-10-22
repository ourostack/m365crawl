//go:build acceptance

package acceptance

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/v8"
)

func owaOraclePreviewText(value any) any {
	text, ok := value.(string)
	if !ok || utf8.ValidString(text) {
		return value
	}
	mask := []byte(text)
	for i := 0; i+2 < len(mask); i++ {
		if mask[i] == 0xed && mask[i+1] >= 0xa0 && mask[i+1] <= 0xbf && mask[i+2] >= 0x80 && mask[i+2] <= 0xbf {
			mask[i], mask[i+1], mask[i+2] = '?', '?', '?'
			i += 2
		}
	}
	if !utf8.Valid(mask) {
		return value
	}
	encoded, err := v8.Canonical(text)
	if err != nil {
		return value
	}
	var normalized string
	if json.Unmarshal(encoded, &normalized) != nil {
		return value
	}
	return normalized
}

//go:build acceptance

package acceptance

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// Only an explicitly supported XML-shaped HTML fragment is proved. x/net/html is not available in this module's unchanged readonly dependency graph. This standard-library parser is not a substitute claim of full HTML/browser rendering support: attributes, namespaces, scripts/styles, unsupported elements, malformed markup and HTML-only syntax are fixed unproved refusals.
func owaIndependentText(ctx context.Context, source string) (string, error) {
	if ctx.Err() != nil || len(source) > owaMaxPayloadBytes {
		return "", errors.New("body_derivation_resource_refused")
	}
	decoder := xml.NewDecoder(owaContextReader{ctx: ctx, reader: strings.NewReader("<owa>" + source + "</owa>")})
	decoder.Entity = map[string]string{"nbsp": "\u00a0"}
	var text strings.Builder
	var stack []string
	started, ended := false, false
	tokens := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || ctx.Err() != nil {
			return "", errors.New("body_derivation_unproved")
		}
		tokens++
		if tokens > owaMaxQueryRows {
			return "", errors.New("body_derivation_resource_refused")
		}
		switch value := token.(type) {
		case xml.StartElement:
			name := value.Name.Local
			if value.Name.Space != "" || len(value.Attr) != 0 || len(stack) >= 64 {
				return "", errors.New("body_derivation_unproved")
			}
			if len(stack) == 0 {
				if started || ended || name != "owa" {
					return "", errors.New("body_derivation_unproved")
				}
				started = true
			} else {
				switch name {
				case "p", "div", "br", "hr":
					text.WriteByte(' ')
				case "b", "strong", "i", "em", "u", "span", "code":
				default:
					return "", errors.New("body_derivation_unproved")
				}
			}
			stack = append(stack, name)
		case xml.EndElement:
			name := value.Name.Local
			if len(stack) == 0 || stack[len(stack)-1] != name {
				return "", errors.New("body_derivation_unproved")
			}
			stack = stack[:len(stack)-1]
			switch name {
			case "p", "div", "br", "hr":
				text.WriteByte(' ')
			case "owa":
				ended = true
			}
		case xml.CharData:
			if len(stack) == 0 {
				return "", errors.New("body_derivation_unproved")
			}
			text.Write(value)
		case xml.Comment:
		default:
			return "", errors.New("body_derivation_unproved")
		}
		if text.Len() > owaMaxPayloadBytes {
			return "", errors.New("body_derivation_resource_refused")
		}
	}
	if !started || !ended || len(stack) != 0 {
		return "", errors.New("body_derivation_unproved")
	}
	return strings.Join(strings.Fields(text.String()), " "), nil
}

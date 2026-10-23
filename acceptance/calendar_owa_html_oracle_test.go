//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

type owaHTMLTextPiece struct {
	text   string
	breaks int
	block  bool
}

func owaCalendarIndependentHTML(ctx context.Context, source string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(source) > owaMaxPayloadBytes || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return "", errors.New("calendar_oracle_html_encoding_or_budget_refused")
	}
	tokenizer := html.NewTokenizer(owaContextReader{ctx: ctx, reader: strings.NewReader(source)})
	tokenizer.SetMaxBuf(owaMaxPayloadBytes)
	var pieces []owaHTMLTextPiece
	hidden := ""
	for tokens := 0; ; tokens++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if tokens > owaMaxHTMLTokens {
			return "", errors.New("calendar_oracle_html_token_budget_refused")
		}
		kind := tokenizer.Next()
		raw := tokenizer.Raw()
		if kind == html.ErrorToken {
			if !errors.Is(tokenizer.Err(), io.EOF) || len(raw) != 0 {
				return "", errors.New("calendar_oracle_html_lexical_unproved")
			}
			break
		}
		if kind == html.CommentToken && (!strings.HasPrefix(string(raw), "<!--") || !strings.HasSuffix(string(raw), "-->")) {
			return "", errors.New("calendar_oracle_html_lexical_unproved")
		}
		token := tokenizer.Token()
		if hidden != "" {
			if kind == html.EndTagToken && token.Data == hidden {
				hidden = ""
			}
			continue
		}
		if kind == html.TextToken {
			pieces = append(pieces, owaHTMLTextPiece{text: token.Data})
			continue
		}
		if kind == html.CommentToken || kind == html.DoctypeToken {
			continue
		}
		if kind != html.StartTagToken && kind != html.EndTagToken && kind != html.SelfClosingTagToken {
			return "", errors.New("calendar_oracle_html_lexical_unproved")
		}
		if len(raw) == 0 || raw[len(raw)-1] != '>' {
			return "", errors.New("calendar_oracle_html_lexical_unproved")
		}
		switch token.Data {
		case "svg", "math", "plaintext", "xmp", "iframe", "noembed", "noframes", "textarea", "title":
			return "", errors.New("calendar_oracle_html_semantics_unproved")
		case "script", "style":
			if kind != html.EndTagToken {
				hidden = token.Data
			}
		case "br":
			pieces = append(pieces, owaHTMLTextPiece{breaks: 1})
		case "p", "div", "li", "ul", "ol", "tr", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "table", "hr":
			pieces = append(pieces, owaHTMLTextPiece{block: true})
		case "emoji", "img":
			if kind == html.EndTagToken {
				continue
			}
			alt, emoji := "", token.Data == "emoji"
			for _, attribute := range token.Attr {
				if attribute.Key == "alt" {
					alt = attribute.Val
				}
				if attribute.Key == "itemtype" && strings.Contains(strings.ToLower(attribute.Val), "emoji") {
					emoji = true
				}
			}
			if emoji {
				pieces = append(pieces, owaHTMLTextPiece{text: alt})
			}
		}
	}
	var output strings.Builder
	pendingBreaks, space, lineStart := 0, false, true
	for _, piece := range pieces {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if piece.block && pendingBreaks == 0 {
			pendingBreaks = 1
		}
		pendingBreaks += piece.breaks
		for _, char := range piece.text {
			if strings.ContainsRune(" \t\n\r\f\u00a0", char) {
				space = true
				continue
			}
			if pendingBreaks != 0 && output.Len() != 0 {
				output.WriteString(strings.Repeat("\n", pendingBreaks))
				lineStart, space = true, false
			}
			pendingBreaks = 0
			if space && !lineStart {
				output.WriteByte(' ')
			}
			output.WriteRune(char)
			lineStart, space = false, false
		}
		if output.Len() > owaMaxPayloadBytes {
			return "", errors.New("calendar_oracle_html_output_budget_refused")
		}
	}
	return output.String(), nil
}

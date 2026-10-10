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

// Independently traverse HTML5 tokens. Text extraction is not CSS/browser visibility: attributes are not fetched/evaluated, script/style raw text and comments are excluded, block boundaries separate runs, and inline text stays adjacent. Invalid encoding, foreign-content tokenization or incomplete lexical tokens are explicitly unproved.
func owaIndependentText(ctx context.Context, source string) (string, error) {
	if ctx.Err() != nil || len(source) > owaMaxPayloadBytes {
		return "", errors.New("body_derivation_resource_refused")
	}
	if !utf8.ValidString(source) || strings.ContainsRune(source, '\x00') {
		return "", errors.New("body_derivation_encoding_unproved")
	}
	tokenizer := html.NewTokenizer(owaContextReader{ctx: ctx, reader: strings.NewReader(source)})
	tokenizer.SetMaxBuf(owaMaxPayloadBytes)
	var output strings.Builder
	hidden := ""
	for tokens := 0; ; tokens++ {
		if ctx.Err() != nil || tokens > owaMaxHTMLTokens {
			return "", errors.New("body_derivation_resource_refused")
		}
		kind := tokenizer.Next()
		raw := tokenizer.Raw()
		if kind == html.ErrorToken {
			if !errors.Is(tokenizer.Err(), io.EOF) || len(raw) != 0 {
				return "", errors.New("body_tokenization_unproved")
			}
			break
		}
		if kind == html.StartTagToken || kind == html.EndTagToken || kind == html.SelfClosingTagToken {
			if len(raw) == 0 || raw[len(raw)-1] != '>' {
				return "", errors.New("body_tokenization_unproved")
			}
		}
		if kind == html.CommentToken && (!strings.HasPrefix(string(raw), "<!--") || !strings.HasSuffix(string(raw), "-->")) {
			return "", errors.New("body_tokenization_unproved")
		}
		token := tokenizer.Token()
		switch kind {
		case html.TextToken:
			if hidden == "" {
				output.WriteString(token.Data)
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name := token.Data
			if name == "svg" || name == "math" {
				return "", errors.New("body_foreign_content_unproved")
			}
			switch name {
			case "plaintext", "xmp", "iframe", "noembed", "noframes", "textarea", "title":
				return "", errors.New("body_rawtext_semantics_unproved")
			}
			if hidden != "" {
				if kind == html.EndTagToken && name == hidden {
					hidden = ""
					output.WriteByte(' ')
				}
				continue
			}
			if name == "script" || name == "style" {
				output.WriteByte(' ')
				if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
					hidden = name
				}
				continue
			}
			if owaTextBoundary(name) {
				output.WriteByte(' ')
			}
		case html.CommentToken, html.DoctypeToken:
		default:
			return "", errors.New("body_tokenization_unproved")
		}
		if output.Len() > owaMaxPayloadBytes {
			return "", errors.New("body_derivation_resource_refused")
		}
	}
	return strings.Join(strings.Fields(output.String()), " "), nil
}

// This extraction contract is characterized by literals, not a call to the production HTMLText helper or an archive-derived expected value.
func owaTextBoundary(name string) bool {
	switch name {
	case "p", "br", "div", "tr", "td", "th", "li", "ul", "ol", "table", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "blockquote", "pre", "title", "head", "body", "html":
		return true
	}
	return false
}

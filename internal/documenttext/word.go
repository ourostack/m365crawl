package documenttext

import (
	"encoding/xml"
	"sort"
	"strings"
)

func (x *extractor) readWord() error {
	references := map[string]string{}
	if err := x.wordPart(x.main, references); err != nil {
		return err
	}
	rels, err := x.relationships(x.main)
	if err != nil {
		return err
	}
	seen := map[string]string{x.main: "document"}
	for _, role := range []string{"header-footer", "footnotes", "endnotes", "comments"} {
		for _, rel := range rels {
			if !strings.HasPrefix(rel.Type, relationNS+"/") {
				if _, referenced := references[rel.ID]; referenced {
					return &ReadError{Code: "unsupported"}
				}
				continue
			}
			kind := strings.TrimPrefix(rel.Type, relationNS+"/")
			if role == "header-footer" {
				expected, referenced := references[rel.ID]
				if !referenced {
					continue
				}
				if kind != expected {
					return &ReadError{Code: "unsupported"}
				}
				delete(references, rel.ID)
			} else if kind != role {
				continue
			}
			if rel.External {
				x.loss("external_part_unmapped")
				continue
			}
			if previous, exists := seen[rel.Target]; exists {
				if previous != kind {
					return &ReadError{Code: "unsupported"}
				}
				continue
			}
			seen[rel.Target] = kind
			root := map[string]string{"header": "hdr", "footer": "ftr", "footnotes": "footnotes", "endnotes": "endnotes", "comments": "comments"}[kind]
			x.roots[rel.Target] = xml.Name{Space: wordNS, Local: root}
			if err := x.wordPart(rel.Target, nil); err != nil {
				return err
			}
		}
	}
	if len(references) > 0 {
		return &ReadError{Code: "malformed"}
	}
	return nil
}

type wordParagraph struct {
	index int
	text  strings.Builder
}

func (x *extractor) wordPart(part string, references map[string]string) error {
	var stack []*wordParagraph
	var completed []Paragraph
	depth := 0
	skipDepth := 0
	textDepth := 0
	index := 0
	err := x.readXML(part, func(token xml.Token) error {
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if skipDepth != 0 {
				return nil
			}
			if token.Name == (xml.Name{Space: "http://schemas.openxmlformats.org/markup-compatibility/2006", Local: "AlternateContent"}) {
				x.loss("embedded_content_unmapped")
				skipDepth = depth
				return nil
			}
			if token.Name.Space != wordNS {
				skipDepth = depth
				return nil
			}
			switch token.Name.Local {
			case "del", "moveFrom", "instrText", "delText":
				x.loss("revision_or_instruction_unmapped")
				skipDepth = depth
			case "drawing", "object", "pict":
				x.loss("embedded_content_unmapped")
				skipDepth = depth
			case "p":
				p := &wordParagraph{index: index}
				index++
				stack = append(stack, p)
			case "t":
				textDepth = depth
				if len(stack) == 0 {
					x.loss("text_without_paragraph_unmapped")
				}
			case "tab", "br", "cr":
				if len(stack) > 0 {
					value := "\n"
					if token.Name.Local == "tab" {
						value = "\t"
					}
					if err := x.chargeText(value); err != nil {
						return err
					}
					stack[len(stack)-1].text.WriteString(value)
				}
			case "headerReference", "footerReference":
				if references == nil {
					return nil
				}
				id := ""
				for _, attr := range token.Attr {
					if attr.Name == (xml.Name{Space: relationNS, Local: "id"}) {
						if id != "" {
							return &ReadError{Code: "unsupported"}
						}
						id = attr.Value
					}
				}
				kind := strings.TrimSuffix(token.Name.Local, "Reference")
				if id == "" || references[id] != "" && references[id] != kind {
					return &ReadError{Code: "unsupported"}
				}
				references[id] = kind
			}
		case xml.CharData:
			if skipDepth == 0 && textDepth != 0 && len(stack) > 0 {
				value := string(token)
				if err := x.chargeText(value); err != nil {
					return err
				}
				stack[len(stack)-1].text.WriteString(value)
			}
		case xml.EndElement:
			switch skipDepth {
			case depth:
				skipDepth = 0
			case 0:
				if textDepth == depth {
					textDepth = 0
				}
				if token.Name == (xml.Name{Space: wordNS, Local: "p"}) {
					p := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					if err := x.chargeText(part); err != nil {
						return err
					}
					completed = append(completed, Paragraph{Part: part, Index: p.index, Text: p.text.String()})
				}
			}
			depth--
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].Index < completed[j].Index })
	x.result.Paragraphs = append(x.result.Paragraphs, completed...)
	return nil
}

func (x *extractor) chargeText(value string) error {
	if err := x.ctx.Err(); err != nil {
		return err
	}
	if int64(len(value)) > x.limits.TextBytes-x.textBytes {
		return &ReadError{Code: "too_large"}
	}
	x.textBytes += int64(len(value))
	return nil
}

func (x *extractor) loss(code string) { x.losses[code]++ }

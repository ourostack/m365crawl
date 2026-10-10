package documenttext

import (
	"encoding/xml"
	"strings"
)

func (x *extractor) readPowerPoint() error {
	var ids []string
	err := x.readXML(x.main, func(token xml.Token) error {
		start, ok := token.(xml.StartElement)
		if !ok || start.Name != (xml.Name{Space: presentationNS, Local: "sldId"}) {
			return nil
		}
		id, err := relationID(start)
		if err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	})
	if err != nil {
		return err
	}
	rels, err := x.relationships(x.main)
	if err != nil {
		return err
	}
	byID := map[string]relationship{}
	for _, rel := range rels {
		byID[rel.ID] = rel
	}
	seen := map[string]string{}
	for _, id := range ids {
		rel, exists := byID[id]
		if !exists {
			return &ReadError{Code: "malformed"}
		}
		if rel.Type != relationNS+"/slide" {
			return &ReadError{Code: "unsupported"}
		}
		if rel.External {
			x.loss("external_part_unmapped")
			continue
		}
		if _, exists := seen[rel.Target]; exists {
			return &ReadError{Code: "unsupported"}
		}
		seen[rel.Target] = "slide"
		x.roots[rel.Target] = xml.Name{Space: presentationNS, Local: "sld"}
		if err := x.presentationPart(rel.Target); err != nil {
			return err
		}
		notes, err := x.relationships(rel.Target)
		if err != nil {
			return err
		}
		foundNotes := false
		for _, note := range notes {
			if note.Type != relationNS+"/notesSlide" {
				continue
			}
			if foundNotes {
				return &ReadError{Code: "unsupported"}
			}
			foundNotes = true
			if note.External {
				x.loss("external_part_unmapped")
				continue
			}
			if role, exists := seen[note.Target]; exists {
				if role != "notes" {
					return &ReadError{Code: "unsupported"}
				}
				continue
			}
			seen[note.Target] = "notes"
			x.roots[note.Target] = xml.Name{Space: presentationNS, Local: "notes"}
			if err := x.presentationPart(note.Target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (x *extractor) presentationPart(part string) error {
	var text strings.Builder
	depth, paragraphDepth, textDepth, skipDepth := 0, 0, 0, 0
	index := 0
	return x.readXML(part, func(token xml.Token) error {
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if skipDepth != 0 {
				return nil
			}
			unmapped := token.Name == (xml.Name{Space: drawingNS, Local: "fld"}) ||
				token.Name == (xml.Name{Space: "http://schemas.openxmlformats.org/markup-compatibility/2006", Local: "AlternateContent"})
			if token.Name.Space == presentationNS {
				switch token.Name.Local {
				case "pic", "oleObj", "graphicFrame", "video", "audio":
					unmapped = true
				}
			}
			if unmapped {
				x.loss("embedded_content_unmapped")
				skipDepth = depth
				return nil
			}
			if token.Name.Space != drawingNS {
				return nil
			}
			switch token.Name.Local {
			case "p":
				if paragraphDepth != 0 {
					return &ReadError{Code: "malformed"}
				}
				x.paragraphCount++
				if x.paragraphCount > x.limits.Paragraphs {
					return &ReadError{Code: "too_large"}
				}
				paragraphDepth = depth
				text.Reset()
			case "t":
				if paragraphDepth == 0 {
					x.loss("text_without_paragraph_unmapped")
				} else {
					textDepth = depth
				}
			case "br":
				if paragraphDepth != 0 {
					if err := x.chargeText("\n"); err != nil {
						return err
					}
					text.WriteByte('\n')
				}
			}
		case xml.CharData:
			if skipDepth == 0 && textDepth != 0 {
				value := string(token)
				if err := x.chargeText(value); err != nil {
					return err
				}
				text.WriteString(value)
			}
		case xml.EndElement:
			if skipDepth == depth {
				skipDepth = 0
			} else if skipDepth == 0 {
				if textDepth == depth {
					textDepth = 0
				}
				if paragraphDepth == depth {
					if err := x.chargeText(part); err != nil {
						return err
					}
					x.result.Paragraphs = append(x.result.Paragraphs, Paragraph{Part: part, Index: index, Text: text.String()})
					index++
					paragraphDepth = 0
				}
			}
			depth--
		}
		return nil
	})
}

package documenttext

import (
	"encoding/xml"
	"strconv"
	"strings"
)

func (x *extractor) readExcel() error {
	var sheetIDs []string
	err := x.readXML(x.main, func(token xml.Token) error {
		start, ok := token.(xml.StartElement)
		if !ok || start.Name != (xml.Name{Space: excelNS, Local: "sheet"}) {
			return nil
		}
		id, err := relationID(start)
		if err != nil {
			return err
		}
		sheetIDs = append(sheetIDs, id)
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
	var shared []string
	sharedSeen := false
	for _, rel := range rels {
		byID[rel.ID] = rel
		if rel.Type != relationNS+"/sharedStrings" {
			continue
		}
		if sharedSeen || rel.External {
			return &ReadError{Code: "unsupported"}
		}
		sharedSeen = true
		shared, err = x.sharedStrings(rel.Target)
		if err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, id := range sheetIDs {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		rel, exists := byID[id]
		if !exists {
			return &ReadError{Code: "malformed"}
		}
		if rel.Type != relationNS+"/worksheet" {
			return &ReadError{Code: "unsupported"}
		}
		if rel.External {
			x.loss("external_part_unmapped")
			continue
		}
		if seen[rel.Target] {
			continue
		}
		seen[rel.Target] = true
		if err := x.worksheet(rel.Target, shared); err != nil {
			return err
		}
	}
	return nil
}

func relationID(start xml.StartElement) (string, error) {
	id := ""
	for _, attr := range start.Attr {
		if attr.Name != (xml.Name{Space: relationNS, Local: "id"}) {
			continue
		}
		if id != "" || attr.Value == "" {
			return "", &ReadError{Code: "unsupported"}
		}
		id = attr.Value
	}
	if id == "" {
		return "", &ReadError{Code: "unsupported"}
	}
	return id, nil
}

func (x *extractor) sharedStrings(part string) ([]string, error) {
	x.roots[part] = xml.Name{Space: excelNS, Local: "sst"}
	var result []string
	var current strings.Builder
	depth, siDepth, textDepth, phoneticDepth := 0, 0, 0, 0
	var retained int64
	err := x.readXML(part, func(token xml.Token) error {
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if token.Name.Space != excelNS {
				return nil
			}
			switch token.Name.Local {
			case "si":
				if siDepth != 0 {
					return &ReadError{Code: "malformed"}
				}
				siDepth = depth
				current.Reset()
			case "rPh":
				if phoneticDepth == 0 {
					phoneticDepth = depth
					x.loss("phonetic_text_unmapped")
				}
			case "t":
				if siDepth != 0 && phoneticDepth == 0 {
					textDepth = depth
				}
			}
		case xml.CharData:
			if textDepth != 0 && phoneticDepth == 0 {
				if int64(len(token)) > x.limits.SharedStringBytes-retained {
					return &ReadError{Code: "too_large"}
				}
				retained += int64(len(token))
				current.Write(token)
			}
		case xml.EndElement:
			if textDepth == depth {
				textDepth = 0
			}
			if phoneticDepth == depth {
				phoneticDepth = 0
			}
			if siDepth == depth {
				if len(result) >= x.limits.Cells {
					return &ReadError{Code: "too_large"}
				}
				result = append(result, current.String())
				siDepth = 0
			}
			depth--
		}
		return nil
	})
	return result, err
}

type worksheetCell struct {
	reference, kind                         string
	value, inline                           strings.Builder
	valueSeen, inlineSeen, formula, invalid bool
}

func (x *extractor) worksheet(part string, shared []string) error {
	x.roots[part] = xml.Name{Space: excelNS, Local: "worksheet"}
	seen := map[string]bool{}
	depth, cellDepth, valueDepth, inlineDepth, textDepth, phoneticDepth := 0, 0, 0, 0, 0, 0
	var cell *worksheetCell
	return x.readXML(part, func(token xml.Token) error {
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if token.Name.Space != excelNS {
				return nil
			}
			if token.Name.Local == "c" {
				if cell != nil {
					return &ReadError{Code: "malformed"}
				}
				x.cellCount++
				if x.cellCount > x.limits.Cells {
					return &ReadError{Code: "too_large"}
				}
				fields, err := selectedAttributes(token, "r", "t", "s")
				if err != nil {
					return err
				}
				cell = &worksheetCell{reference: fields["r"], kind: fields["t"]}
				if cell.kind == "" {
					cell.kind = "n"
				}
				cell.invalid = !validCellReference(cell.reference) || seen[cell.reference]
				seen[cell.reference] = true
				cellDepth = depth
				if fields["s"] != "" {
					x.loss("formatting_or_external_values_unmapped")
				}
				return nil
			}
			if cell == nil {
				return nil
			}
			switch token.Name.Local {
			case "v":
				if cell.valueSeen {
					cell.invalid = true
				}
				cell.valueSeen = true
				valueDepth = depth
			case "is":
				if cell.inlineSeen {
					cell.invalid = true
				}
				cell.inlineSeen = true
				inlineDepth = depth
			case "t":
				if inlineDepth != 0 {
					textDepth = depth
				}
			case "rPh":
				phoneticDepth = depth
				x.loss("phonetic_text_unmapped")
			case "f":
				cell.formula = true
			}
		case xml.CharData:
			if cell == nil {
				return nil
			}
			if valueDepth != 0 {
				if int64(cell.value.Len())+int64(len(token)) > x.limits.MemberBytes {
					return &ReadError{Code: "too_large"}
				}
				cell.value.Write(token)
			} else if textDepth != 0 && phoneticDepth == 0 {
				if int64(cell.inline.Len())+int64(len(token)) > x.limits.MemberBytes {
					return &ReadError{Code: "too_large"}
				}
				cell.inline.Write(token)
			}
		case xml.EndElement:
			if cellDepth == depth {
				if err := x.finishCell(part, cell, shared); err != nil {
					return err
				}
				cell = nil
				cellDepth = 0
			}
			if valueDepth == depth {
				valueDepth = 0
			}
			if textDepth == depth {
				textDepth = 0
			}
			if inlineDepth == depth {
				inlineDepth = 0
			}
			if phoneticDepth == depth {
				phoneticDepth = 0
			}
			depth--
		}
		return nil
	})
}

func (x *extractor) finishCell(part string, c *worksheetCell, shared []string) error {
	value := c.value.String()
	switch c.kind {
	case "s":
		index, err := strconv.ParseUint(value, 10, 64)
		if err != nil || value == "" || len(value) > 1 && value[0] == '0' || value[0] < '0' || value[0] > '9' || index >= uint64(len(shared)) || c.inlineSeen {
			c.invalid = true
		} else {
			value = shared[index]
		}
	case "inlineStr":
		if !c.inlineSeen || c.valueSeen {
			c.invalid = true
		}
		value = c.inline.String()
	case "n", "b", "e", "str", "d":
		c.invalid = c.invalid || c.inlineSeen
	default:
		c.invalid = true
	}
	if c.invalid {
		x.loss("cell_unmapped")
		return nil
	}
	if c.formula && !c.valueSeen {
		x.loss("formula_without_cache")
	}
	for _, field := range []string{part, c.reference, c.kind, value} {
		if err := x.chargeText(field); err != nil {
			return err
		}
	}
	x.result.Cells = append(x.result.Cells, Cell{Part: part, Reference: c.reference, Type: c.kind, Value: value, FormulaCached: c.formula && c.valueSeen})
	return nil
}

func validCellReference(value string) bool {
	if len(value) < 2 || len(value) > 32 {
		return false
	}
	i := 0
	for i < len(value) && value[i] >= 'A' && value[i] <= 'Z' {
		i++
	}
	if i == 0 || i == len(value) || value[i] < '1' || value[i] > '9' {
		return false
	}
	for i++; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

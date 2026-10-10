package documenttext

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"path"
	"strings"
)

const (
	packageNS      = "http://schemas.openxmlformats.org/package/2006/relationships"
	contentNS      = "http://schemas.openxmlformats.org/package/2006/content-types"
	relationNS     = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	wordNS         = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	excelNS        = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	presentationNS = "http://schemas.openxmlformats.org/presentationml/2006/main"
	drawingNS      = "http://schemas.openxmlformats.org/drawingml/2006/main"
)

type extractor struct {
	ctx            context.Context
	limits         Limits
	main           string
	members        map[string][]*zip.File
	roots          map[string]xml.Name
	selected       map[string]bool
	selectedBytes  int64
	tokens         int
	relationCache  map[string][]relationship
	result         Result
	losses         map[string]int
	textBytes      int64
	paragraphCount int
}

type relationship struct {
	ID, Type, Target string
	External         bool
}

func openPackage(ctx context.Context, r io.ReaderAt, size int64, kind string, limits Limits) (*extractor, error) {
	if err := preflightZIP(ctx, r, size, limits.Entries); err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(r, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, safeReadError(err)
	}
	x := &extractor{
		ctx: ctx, limits: limits, members: map[string][]*zip.File{},
		roots: map[string]xml.Name{
			"_rels/.rels":         {Space: packageNS, Local: "Relationships"},
			"[Content_Types].xml": {Space: contentNS, Local: "Types"},
		},
		selected: map[string]bool{}, relationCache: map[string][]relationship{},
		result: Result{Kind: kind, State: "text_observations"}, losses: map[string]int{},
	}
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		x.members[file.Name] = append(x.members[file.Name], file)
	}
	if len(x.members["_rels/.rels"]) == 0 || len(x.members["[Content_Types].xml"]) == 0 {
		return nil, &ReadError{Code: "unsupported"}
	}
	rels, err := x.relationships("")
	if err != nil {
		return nil, err
	}
	for _, rel := range rels {
		if rel.Type != relationNS+"/officeDocument" {
			continue
		}
		if x.main != "" || rel.External {
			return nil, &ReadError{Code: "unsupported"}
		}
		x.main = rel.Target
	}
	type packageKind struct{ main, namespace, root, contentType string }
	kinds := map[string]packageKind{
		"docx": {"word/document.xml", wordNS, "document", "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"},
		"xlsx": {"xl/workbook.xml", excelNS, "workbook", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"},
		"pptx": {"ppt/presentation.xml", presentationNS, "presentation", "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"},
	}
	wanted, exists := kinds[kind]
	if !exists || x.main != wanted.main {
		return nil, &ReadError{Code: "unsupported"}
	}
	x.roots[x.main] = xml.Name{Space: wanted.namespace, Local: wanted.root}
	if _, err := x.selectPart(x.main); err != nil {
		return nil, err
	}
	actualType, err := x.contentType(x.main)
	if err != nil {
		return nil, err
	}
	if actualType != wanted.contentType {
		return nil, &ReadError{Code: "unsupported"}
	}
	return x, nil
}

func (x *extractor) relationships(part string) ([]relationship, error) {
	if cached, ok := x.relationCache[part]; ok {
		return cached, nil
	}
	name := "_rels/.rels"
	if part != "" {
		name = path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
	}
	if len(x.members[name]) == 0 {
		x.relationCache[part] = nil
		return nil, nil
	}
	x.roots[name] = xml.Name{Space: packageNS, Local: "Relationships"}
	var result []relationship
	seen := map[string]bool{}
	depth := 0
	err := x.readXML(name, func(token xml.Token) error {
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth != 2 || token.Name != (xml.Name{Space: packageNS, Local: "Relationship"}) {
				return nil
			}
			fields, err := selectedAttributes(token, "Id", "Type", "Target", "TargetMode")
			if err != nil {
				return err
			}
			if fields["Id"] == "" || fields["Type"] == "" || fields["Target"] == "" || seen[fields["Id"]] {
				return &ReadError{Code: "unsupported"}
			}
			seen[fields["Id"]] = true
			mode := fields["TargetMode"]
			if mode != "" && mode != "Internal" && mode != "External" {
				return &ReadError{Code: "unsupported"}
			}
			rel := relationship{ID: fields["Id"], Type: fields["Type"], Target: fields["Target"], External: mode == "External"}
			if !rel.External {
				rel.Target, err = internalTarget(part, rel.Target)
				if err != nil {
					return err
				}
			}
			result = append(result, rel)
		case xml.EndElement:
			depth--
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	x.relationCache[part] = result
	return result, nil
}

func (x *extractor) contentType(part string) (string, error) {
	overrides, defaults := map[string]string{}, map[string]string{}
	depth := 0
	err := x.readXML("[Content_Types].xml", func(token xml.Token) error {
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth != 2 || token.Name.Space != contentNS {
				return nil
			}
			switch token.Name.Local {
			case "Override":
				fields, err := selectedAttributes(token, "PartName", "ContentType")
				if err != nil {
					return err
				}
				if !strings.HasPrefix(fields["PartName"], "/") || fields["ContentType"] == "" {
					return &ReadError{Code: "unsupported"}
				}
				name, err := internalTarget("", fields["PartName"])
				if err != nil {
					return err
				}
				if _, exists := overrides[name]; exists {
					return &ReadError{Code: "unsupported"}
				}
				overrides[name] = fields["ContentType"]
			case "Default":
				fields, err := selectedAttributes(token, "Extension", "ContentType")
				if err != nil {
					return err
				}
				extension := strings.ToLower(fields["Extension"])
				if extension == "" || fields["ContentType"] == "" {
					return &ReadError{Code: "unsupported"}
				}
				if _, exists := defaults[extension]; exists {
					return &ReadError{Code: "unsupported"}
				}
				defaults[extension] = fields["ContentType"]
			}
		case xml.EndElement:
			depth--
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if contentType, exists := overrides[part]; exists {
		return contentType, nil
	}
	return defaults[strings.TrimPrefix(path.Ext(part), ".")], nil
}

func selectedAttributes(element xml.StartElement, names ...string) (map[string]string, error) {
	fields := make(map[string]string, len(names))
	for _, attr := range element.Attr {
		if attr.Name.Space != "" {
			continue
		}
		for _, name := range names {
			if attr.Name.Local != name {
				continue
			}
			if _, exists := fields[name]; exists {
				return nil, &ReadError{Code: "unsupported"}
			}
			fields[name] = attr.Value
		}
	}
	return fields, nil
}

func internalTarget(part, target string) (string, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || strings.ContainsAny(u.Path, "\\\x00") {
		return "", &ReadError{Code: "unsupported"}
	}
	var components []string
	if !strings.HasPrefix(u.Path, "/") && part != "" {
		directory := path.Dir(part)
		if directory != "." {
			components = strings.Split(directory, "/")
		}
	}
	for _, component := range strings.Split(strings.TrimPrefix(u.Path, "/"), "/") {
		switch component {
		case "":
			return "", &ReadError{Code: "unsupported"}
		case ".":
		case "..":
			if len(components) == 0 {
				return "", &ReadError{Code: "unsupported"}
			}
			components = components[:len(components)-1]
		default:
			components = append(components, component)
		}
	}
	if len(components) == 0 {
		return "", &ReadError{Code: "unsupported"}
	}
	return strings.Join(components, "/"), nil
}

func (x *extractor) selectPart(part string) (*zip.File, error) {
	files := x.members[part]
	if len(files) == 0 {
		return nil, &ReadError{Code: "malformed"}
	}
	if len(files) != 1 {
		return nil, &ReadError{Code: "unsupported"}
	}
	file := files[0]
	if !file.Mode().IsRegular() || file.Flags&1 != 0 || (file.Method != zip.Store && file.Method != zip.Deflate) {
		return nil, &ReadError{Code: "unsupported"}
	}
	if !x.selected[part] {
		if len(x.selected) >= x.limits.Parts || file.UncompressedSize64 > uint64(x.limits.MemberBytes) || file.UncompressedSize64 > uint64(x.limits.SelectedBytes-x.selectedBytes) {
			return nil, &ReadError{Code: "too_large"}
		}
		x.selected[part] = true
		x.selectedBytes += int64(file.UncompressedSize64)
	}
	return file, nil
}

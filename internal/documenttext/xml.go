package documenttext

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
)

func (x *extractor) readXML(part string, visit func(xml.Token) error) (err error) {
	if err := x.ctx.Err(); err != nil {
		return err
	}
	expected, ok := x.roots[part]
	if !ok {
		return &ReadError{Code: "unsupported"}
	}
	file, err := x.selectPart(part)
	if err != nil {
		return err
	}
	source, err := file.Open()
	if err != nil {
		return safeReadError(err)
	}
	defer func() {
		if closeErr := source.Close(); err == nil && closeErr != nil {
			err = safeReadError(closeErr)
		}
		if cancelled := x.ctx.Err(); cancelled != nil {
			err = cancelled
		}
	}()
	limited := &io.LimitedReader{R: source, N: x.limits.MemberBytes + 1}
	buffered := bufio.NewReader(limited)
	if prefix, _ := buffered.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = buffered.Discard(3)
	}
	decoder := xml.NewDecoder(buffered)
	depth := 0
	rootSeen := false
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		token, readErr := decoder.Token()
		if limited.N == 0 {
			return &ReadError{Code: "too_large"}
		}
		if errors.Is(readErr, io.EOF) {
			if !rootSeen || depth != 0 {
				return &ReadError{Code: "malformed"}
			}
			return nil
		}
		if readErr != nil {
			return safeReadError(readErr)
		}
		x.tokens++
		if x.tokens > x.limits.XMLTokens {
			return &ReadError{Code: "too_large"}
		}
		switch token := token.(type) {
		case xml.StartElement:
			if (expected.Space == wordNS && token.Name == (xml.Name{Space: wordNS, Local: "p"})) ||
				(expected.Space == presentationNS && token.Name == (xml.Name{Space: drawingNS, Local: "p"})) {
				x.paragraphCount++
				if x.paragraphCount > x.limits.Paragraphs {
					return &ReadError{Code: "too_large"}
				}
			}
			if expected == (xml.Name{Space: excelNS, Local: "worksheet"}) && token.Name == (xml.Name{Space: excelNS, Local: "c"}) {
				x.cellCount++
				if x.cellCount > x.limits.Cells {
					return &ReadError{Code: "too_large"}
				}
			}
			if depth == 0 {
				if rootSeen {
					return &ReadError{Code: "malformed"}
				}
				if token.Name != expected {
					return &ReadError{Code: "unsupported"}
				}
				rootSeen = true
			}
			depth++
			if depth > x.limits.Depth {
				return &ReadError{Code: "too_large"}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(token)) != 0 {
				return &ReadError{Code: "malformed"}
			}
		case xml.Directive:
			return &ReadError{Code: "unsupported"}
		case xml.ProcInst:
			if token.Target != "xml" || rootSeen {
				return &ReadError{Code: "unsupported"}
			}
		}
		if err := visit(token); err != nil {
			return safeReadError(err)
		}
	}
}

func safeReadError(err error) error {
	var readErr *ReadError
	if errors.As(err, &readErr) {
		return readErr
	}
	var syntax *xml.SyntaxError
	if errors.As(err, &syntax) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, zip.ErrFormat) || errors.Is(err, zip.ErrChecksum) {
		return &ReadError{Code: "malformed"}
	}
	return &ReadError{Code: "read_failed"}
}

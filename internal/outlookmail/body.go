package outlookmail

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
)

// Body states. The mapper gives inline, file, none and unreadable (inline text that is not
// UTF-8); Collect adds missing and unreadable for a file it could not read.
const (
	BodyInline     = "inline"
	BodyFile       = "file"
	BodyMissing    = "missing"
	BodyUnreadable = "unreadable"
	BodyNone       = "none"
	// BodyNotRead is a file body that Collect left unread (Options.ReadBodies off, or NeedBody
	// said no). MapBody itself reports such a body as BodyFile with its Path; after Collect,
	// BodyFile means the file was read and HTML holds its text, and BodyNotRead means HTML is nil
	// because nothing was read, which says nothing about the file.
	BodyNotRead = "not_read"
)

// MaxBodyBytes caps the inflated size of one body file.
const MaxBodyBytes = 16 << 20

const (
	// A body record's word table sits in the fixed region [0, TagBody); a string target is
	// counted from TagBody plus the lead word.
	bodyFilesPrefix = "~/Files/"
	bodyScanEnd     = TagBody
)

// Body is what a class 0xca object holds: inline HTML, or the path of the file that holds it.
type Body struct {
	HTML  []byte
	Path  string
	State string
}

// candidate is a word pair whose target looks like a body.
type candidate struct {
	ok      bool
	flagged bool // the length word has bit 31 set, as every inline body and path measured does
	pos, n  int
}

// offer replaces the candidate when the new pair is better: one with bit 31 set beats one
// without (a small integer pair in the fixed region can point at the start of the string area
// by accident), and the longer of two equal kinds wins; a tie keeps the first.
func (c *candidate) offer(flagged bool, pos, n int) {
	if !c.ok || flagged && !c.flagged || flagged == c.flagged && n > c.n {
		*c = candidate{ok: true, flagged: flagged, pos: pos, n: n}
	}
}

// MapBody maps a class 0xca object. The word table is scanned: every 4-byte-aligned word pair in
// the fixed region whose target (the offset word plus the string area base) lies inside the
// object. A target that is UTF-8 starting, after whitespace and a byte order mark, with '<' is
// inline HTML (bytes that are not valid UTF-8 give State unreadable); a target that is UTF-16LE
// text starting with ~/Files/ is a file path. Inline wins over a path. Among several matches the
// pair with bit 31 set in its length word wins, then the longest, then the first. The offsets of
// the pair differ between variants of the record, so none is pinned.
func MapBody(o hxstore.Object) Body {
	f, err := newFields(o, TagBody)
	if err != nil {
		return Body{State: BodyNone}
	}
	var html, path candidate
	for off := 0; off+8 <= bodyScanEnd; off += 4 {
		w, l := word(o, off), word(o, off+4)
		n, flagged := int(l&lengthMask), l&^lengthMask != 0
		if n == 0 {
			continue
		}
		raw, ok := o.Bytes(int(w)+f.base, n)
		switch {
		case !ok:
		case sniffHTML(raw):
			html.offer(flagged, int(w)+f.base, n)
		case filePath(raw) != "":
			path.offer(flagged, int(w)+f.base, n)
		}
	}
	switch {
	case html.ok:
		raw, _ := o.Bytes(html.pos, html.n) // checked above
		raw = bytes.TrimRight(raw, "\x00")  // the stated length can count terminating NULs
		if !utf8.Valid(raw) {
			return Body{State: BodyUnreadable}
		}
		html := append([]byte(nil), raw...)
		return Body{HTML: html, State: BodyInline}
	case path.ok:
		raw, _ := o.Bytes(path.pos, path.n)
		return Body{Path: filePath(raw), State: BodyFile}
	}
	return Body{State: BodyNone}
}

// sniffHTML reports whether raw, after whitespace and a UTF-8 byte order mark, starts with '<'.
func sniffHTML(raw []byte) bool {
	raw = bytes.TrimPrefix(bytes.TrimLeft(raw, " \t\r\n"), []byte{0xef, 0xbb, 0xbf})
	raw = bytes.TrimLeft(raw, " \t\r\n")
	return len(raw) > 0 && raw[0] == '<'
}

// filePath reads raw as a UTF-16LE path with a terminator, and returns it when it starts with
// ~/Files/.
func filePath(raw []byte) string {
	if len(raw)%2 != 0 {
		return ""
	}
	s, ok := hxstore.Object{Raw: raw}.StringAtUnaligned(0)
	if !ok || !strings.HasPrefix(s, bodyFilesPrefix) {
		return ""
	}
	return s
}

// The reasons ReadDat fails. ErrBodyMissing is a file that is not there; every other error is
// an unreadable body.
var (
	ErrBodyMissing  = errors.New("outlookmail: body file not found")
	ErrBodyTooLarge = errors.New("outlookmail: body file is larger than the cap")
	ErrBodyPath     = errors.New("outlookmail: body path is not inside the profile's Files directory")
	ErrBodyFormat   = errors.New("outlookmail: body file is not readable gzip")
)

// openDat is the seam for reading a body file: outlookdesktop's read-only open.
var openDat = outlookdesktop.OpenReadOnly

// ReadDat reads the gzip body file that path (as ~/Files/...) names under the Outlook profile
// directory root, inflates it up to limit bytes and returns the bytes. It never writes: the file
// is opened read-only through outlookdesktop. The path must start with ~/Files/ and have no
// backslash, colon, NUL, empty, "." or ".." element; the resolved file (symbolic links followed)
// must lie inside root/Files. An empty root is ErrBodyPath. A file that is gone is ErrBodyMissing; the file must start with
// the gzip magic; an inflated size over limit is ErrBodyTooLarge.
func ReadDat(root, path string, limit int64) ([]byte, error) {
	rel, ok := strings.CutPrefix(path, bodyFilesPrefix)
	if root == "" || !ok || !cleanRel(rel) {
		return nil, ErrBodyPath
	}
	base, err := filepath.EvalSymlinks(filepath.Join(root, "Files"))
	if err != nil {
		return nil, fileError(err)
	}
	full, err := filepath.EvalSymlinks(filepath.Join(base, filepath.FromSlash(rel)))
	if err != nil {
		return nil, fileError(err)
	}
	if inside, rerr := filepath.Rel(base, full); rerr != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return nil, ErrBodyPath
	}
	f, err := openDat(full)
	if err != nil {
		return nil, fileError(err)
	}
	defer func() { _ = f.Close() }()
	var magic [2]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || magic != [2]byte{0x1f, 0x8b} {
		return nil, ErrBodyFormat
	}
	zr, err := gzip.NewReader(io.MultiReader(bytes.NewReader(magic[:]), f))
	if err != nil {
		return nil, ErrBodyFormat
	}
	data, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, ErrBodyFormat
	}
	if int64(len(data)) > limit {
		return nil, ErrBodyTooLarge
	}
	// ReadAll grows by doubling, so its result can hold up to twice the text. The body stays in the
	// result until the commit, for every message at once: keep only what it needs.
	return slices.Clip(bytes.Clone(data)), nil
}

// fileError maps an operating system error to ErrBodyMissing for a file that is gone, and to a
// fixed message otherwise (the system's text can name a path, so it is not passed on).
func fileError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrBodyMissing
	}
	return errors.New("outlookmail: body file cannot be opened")
}

// cleanRel reports whether the part of a body path after ~/Files/ is a plain relative path:
// non-empty elements separated by '/', none of them "." or "..", with no backslash, colon or NUL.
func cleanRel(rel string) bool {
	if rel == "" || strings.ContainsAny(rel, "\\:\x00") {
		return false
	}
	for _, el := range strings.Split(rel, "/") {
		if el == "" || el == "." || el == ".." {
			return false
		}
	}
	return true
}

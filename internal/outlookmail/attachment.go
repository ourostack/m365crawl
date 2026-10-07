package outlookmail

import (
	"path"
	"strings"

	"github.com/ourostack/m365crawl/internal/hxstore"
)

// Attachment fixed region (class 0x16a).
const (
	atSize    = 568
	atName    = 608
	atState   = 624
	atPath    = 648
	atMessage = 380

	// downloadedState is the value at +624 of an attachment whose file exists (likely: it agreed
	// with file presence on every attachment of a measured copy).
	downloadedState = 2
)

// Attachment is an attachment record. Inline marks a bare-GUID name with no extension, which is
// an embedded item or image.
type Attachment struct {
	Key         uint32
	MessageKey  uint32 // the detail key of the message
	Name        string
	Size        int64
	ContentType string
	Inline      bool
	Downloaded  bool
	Path        string // ~/Files/... under the profile, empty when the store names none
}

// MapAttachment maps a class 0x16a object.
func MapAttachment(o hxstore.Object) (Attachment, error) {
	f, err := newFields(o, TagAttachment)
	if err != nil {
		return Attachment{}, err
	}
	a := Attachment{
		Key: word(o, offKey), MessageKey: word(o, atMessage), Size: int64(word(o, atSize)),
		Name: f.text(atName), Path: f.text(atPath), Downloaded: word(o, atState) == downloadedState,
	}
	if err := f.err(); err != nil {
		return Attachment{}, err
	}
	a.ContentType, a.Inline = contentType(a.Name), bareGUID(a.Name)
	return a, nil
}

var contentTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".ics": "text/calendar", ".pdf": "application/pdf", ".txt": "text/plain", ".zip": "application/zip",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

// contentType gives the type from a fixed table of extensions, else application/octet-stream.
func contentType(name string) string {
	if t, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// bareGUID reports whether name is a GUID and nothing else: 32 hex digits, or the 8-4-4-4-12
// form with hyphens, with or without a surrounding pair of braces, and no extension.
func bareGUID(name string) bool {
	braced := strings.HasPrefix(name, "{") && strings.HasSuffix(name, "}") && len(name) >= 2
	if braced {
		name = name[1 : len(name)-1]
	}
	if strings.ContainsAny(name, "{}") {
		return false
	}
	switch len(name) {
	case 32:
		return allHex(name)
	case 36:
		return name[8] == '-' && name[13] == '-' && name[18] == '-' && name[23] == '-' &&
			allHex(name[:8]+name[9:13]+name[14:18]+name[19:23]+name[24:])
	}
	return false
}

func allHex(s string) bool {
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

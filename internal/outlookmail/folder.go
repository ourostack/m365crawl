package outlookmail

import "github.com/ourostack/m365crawl/internal/hxstore"

// Folder fixed region (class 0x4d).
const (
	fdName = 1088
	fdType = 1160
)

// Folder is a mail folder. Kind is inbox, archive, drafts, sent, deleted, junk, to_me or other.
// The mapper emits junk_or_to_me for the well-known type shared by Junk Email and To Me, which
// Collect resolves.
type Folder struct {
	Key    uint32
	Parent uint32
	Name   string
	Kind   string
	// BadStrings counts string fields that were present but unreadable and were left blank.
	BadStrings int
}

// KindJunkOrToMe is the kind the mapper gives type 0x7a.
const KindJunkOrToMe = "junk_or_to_me"

// KindUnknown is the kind of a message whose folder object the store does not hold.
const KindUnknown = "unknown"

var folderKinds = map[uint32]string{
	0x61: "inbox", 0x63: "archive", 0x64: "drafts", 0x65: "sent", 0x67: "deleted", 0x7a: KindJunkOrToMe,
}

// MapFolder maps a class 0x4d object. An unknown well-known type is kind other. The type values
// are likely, not established, outside the English folder names they were measured on.
func MapFolder(o hxstore.Object) (Folder, error) {
	f, err := newFields(o, TagFolder)
	if err != nil {
		return Folder{}, err
	}
	fd := Folder{Key: word(o, offKey), Parent: word(o, offLink), Name: f.text(fdName), Kind: "other"}
	fd.BadStrings = f.bad
	if k, ok := folderKinds[word(o, fdType)]; ok {
		fd.Kind = k
	}
	return fd, nil
}

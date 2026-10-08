package outlookmail

import "github.com/ourostack/m365crawl/internal/hxstore"

// Folder fixed region (class 0x4d).
const (
	fdName = 1088
	fdType = 1160
)

// Folder is a mail folder. Kind is inbox, archive, drafts, sent, deleted, to_me or other. Junk
// Email is not detected: the store gives it the generic folder type 0x7a that user-created
// folders and several other system folders also have, so it is other.
type Folder struct {
	Key    uint32
	Parent uint32
	Name   string
	Kind   string
	// Generic is true for the generic folder type 0x7a. Collect gives such a folder kind to_me
	// when it shares messages with an inbox folder.
	Generic bool
	// BadStrings counts string fields that were present but unreadable and were left blank.
	BadStrings int
}

// typeGeneric is the folder type of user-created folders and of several system folders, Junk
// Email and To Me among them.
const typeGeneric = 0x7a

// KindUnknown is the kind of a message whose folder object the store does not hold.
const KindUnknown = "unknown"

var folderKinds = map[uint32]string{
	0x61: "inbox", 0x63: "archive", 0x64: "drafts", 0x65: "sent", 0x67: "deleted",
}

// MapFolder maps a class 0x4d object. The generic type 0x7a and an unknown well-known type are
// kind other. The type values are likely, not established, outside the English folder names they
// were measured on.
func MapFolder(o hxstore.Object) (Folder, error) {
	f, err := newFields(o, TagFolder)
	if err != nil {
		return Folder{}, err
	}
	fd := Folder{Key: word(o, offKey), Parent: word(o, offLink), Name: f.text(fdName), Kind: "other"}
	fd.BadStrings = f.bad
	fd.Generic = word(o, fdType) == typeGeneric
	if k, ok := folderKinds[word(o, fdType)]; ok {
		fd.Kind = k
	}
	return fd, nil
}

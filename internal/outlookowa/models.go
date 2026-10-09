// Package outlookowa decodes allowlisted OWA mail from a caller-owned private IndexedDB snapshot without allocating archive keys or opening attachment payloads.
package outlookowa

import (
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

const MapperVersion = 1

// Folder retains native folder and parent identities until Normalize binds them.
type Folder struct {
	ID, ParentID string
	Value        outlookmail.Folder
}

// Message retains native header, folder and metadata-only attachment identities.
type Message struct {
	ID, FolderID  string
	AttachmentIDs []string
	Value         outlookmail.Message
}

// Result separates opaque identities from common mail values; losses or doubtful attribution prohibit trusted absence.
type Result struct {
	Folders  []Folder
	Messages []Message
	SeenIDs  []string
	RootID   string
	Notes    outlookmail.Notes
	Losses   []outlookmail.Loss
	Doubtful bool
}

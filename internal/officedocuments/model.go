package officedocuments

import (
	"regexp"
	"time"
)

type Surface string

const (
	Recent      Surface = "recent"
	Shared      Surface = "shared"
	Recommended Surface = "recommended"
	Dialog      Surface = "dialog"
)

type Timestamp struct {
	Raw   string
	Value time.Time
}

type Person struct {
	UPN, DisplayName string
}

type SharePoint struct {
	TenantID, SiteID, WebID, ListID, ListItemID, UniqueID string
}

type Activity struct {
	MessageFormat string
	Users         []Person
	At            Timestamp
}

type Sharing struct {
	DisplayName, Email string
	At                 Timestamp
	Type               *int64
}

type Document struct {
	Ordinal                                                 int
	Surface                                                 Surface
	Title, URL, WebURL, Extension, ResourceID, FriendlyPath string
	Size                                                    *int64
	Pinned                                                  *bool
	SharingState                                            *int64
	DriveID, ItemID                                         string
	SharePoint                                              SharePoint
	SiteURL, SiteTitle, TeamsChannelURL, TeamsChannelTitle  string
	Creator, Modifier                                       Person
	Opened, Created, Modified                               Timestamp
	Activity                                                *Activity
	Sharing                                                 *Sharing
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Documents []Document
	Losses    []Loss
}

var timestampShape = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func timestamp(raw string) (Timestamp, bool) {
	result := Timestamp{Raw: raw}
	if !timestampShape.MatchString(raw) {
		return result, false
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || value.Year() < 1 {
		return result, false
	}
	result.Value = value.UTC()
	return result, true
}

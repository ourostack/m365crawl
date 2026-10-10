package officedocuments

import "time"

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

func timestamp(raw string) (Timestamp, bool) {
	result := Timestamp{Raw: raw}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || value.Year() < 1 {
		return result, false
	}
	result.Value = value.UTC()
	return result, true
}

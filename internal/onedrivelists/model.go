package onedrivelists

type Scope struct {
	SiteID, WebID, ListID, DriveID, Title, SiteURL, ListURL string
	LastSyncRaw                                             *int64
	InventoryPresent                                        bool
}

type Item struct {
	SiteID, WebID, ListID                            string
	ID                                               int64
	UniqueID, Path, Name, ServerURL, Extension, Kind string
	CreatedRaw, ModifiedRaw, LastModifiedRaw         string
	AuthorID, EditorID                               *int64
}

type User struct {
	SiteID                  string
	ID                      int64
	Title, Email, Name, SIP string
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Scopes []Scope
	Items  []Item
	Users  []User
	Losses []Loss
}

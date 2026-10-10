package onedrivesync

type Scope struct {
	ID, SourceResourceID, SiteID, WebID, ListID, WebURL, RemotePath, LastKnownFolderPath string
}

type File struct {
	ID, ParentID, Name                                       string
	SizeRaw, ChangedRaw, ServerChangedRaw, StatusRaw, PinRaw *int64
}

type Folder struct{ ID, ParentID, ParentScopeID, Name string }
type Graph struct{ ResourceID, CreatedBy, ModifiedBy, CompositeID string }

type Policy struct {
	SiteID, WebID, ListID, DriveID, SiteTitle, LibraryTitle           string
	ViewURLTemplate, ShareURLTemplate, WebURLTemplate, DAVURLTemplate string
}

type Hydration struct {
	ResourceID                  string
	FirstRaw, LastRaw, CountRaw *int64
	TypeRaw                     string
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Scopes    []Scope
	Files     []File
	Folders   []Folder
	Graph     []Graph
	Policies  []Policy
	Hydration []Hydration
	Losses    []Loss
}

type ReadError struct{ Code string }

func (e *ReadError) Error() string { return e.Code }

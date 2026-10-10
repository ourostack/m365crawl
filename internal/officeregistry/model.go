package officeregistry

type Observation struct {
	NodeID                              int64
	ApplicationPath, ApplicationValue   string
	Title, URL, FriendlyPath, OpenedRaw string
	Size                                *int64
	Pinned                              *bool
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Documents []Observation
	Losses    []Loss
}

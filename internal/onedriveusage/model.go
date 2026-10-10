package onedriveusage

type RecentObservation struct {
	ID, ItemID, ListID, WebID, SiteID, Type, Source, Extension, WorkingSetID   string
	LastModifiedRaw, LastAccessedRaw, FileModifiedRaw, LastSharedRaw, SavedRaw string
	HiddenFromShared, LocalPlaceholder                                         *int64
}

type Person struct{ DisplayName, SMTP string }

type Share struct {
	DisplayName, SMTP, AadID, AtRaw, ConversationID, Subject, AttachmentID string
	Participants                                                           []Person
	ParticipantsCount                                                      *int64
	MeetingStartRaw, ICalUID, MeetingSubject                               string
	Recurring                                                              *bool
}

type DocumentObservation struct {
	ID, Format, Title, URL, Extension, Owner, CreatedRaw, ModifiedRaw string
	SiteID, WebID, ListID, UniqueID                                   string
	Size                                                              *int64
	Latest                                                            *Share
	History                                                           []Share
}

type Collaborator struct {
	ID, UserID, DisplayName, Department, JobTitle, Office string
	Emails                                                []string
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Recent        []RecentObservation
	Documents     []DocumentObservation
	Collaborators []Collaborator
	Losses        []Loss
}

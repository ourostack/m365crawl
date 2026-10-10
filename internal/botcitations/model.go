package botcitations

type Citation struct {
	Position             int
	ID                   int64
	Title                string
	Link, ContentType    *string
	NestedContentPresent bool
}

type Observation struct {
	MessageID, SenderID       string
	AppID, AppName, ReplyToID *string
	Citations                 []Citation
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Observation Observation
	Losses      []Loss
}

type MapError struct{ Code string }

func (e *MapError) Error() string { return e.Code }

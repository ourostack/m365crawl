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
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type Result struct {
	Observation Observation
	Losses      []Loss
}

type MapError struct{ Code string }

func (e *MapError) Error() string { return e.Code }

// LossMeanings explains, in one line each, every loss code MapMessage can count. It is the one
// table the help and each message's meaning read, and a test refuses a code that is emitted
// without a line here.
var LossMeanings = map[string]string{
	"teams_bot_properties_unmapped":       "the message's properties is not an object, so no app, reply or citation claim was read",
	"teams_bot_metadata_unmapped":         "fromAppMetadata or botMetadata is not an object, so its fields were not read",
	"teams_bot_metadata_field_unmapped":   "an app id, app name or reply-to id is not a string, so that field is null",
	"teams_bot_citations_unmapped":        "botCitations is not a list, so no citation was read",
	"teams_bot_citation_unmapped":         "a citation row is not an object, or its id, title, link, contentType or content has the wrong type, so the row was left out",
	"teams_bot_citation_content_unmapped": "a citation that was read also carries nested content, which is not projected; its id, title and link are shown and nested_content_present is true",
}

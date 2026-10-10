package sharepointcontent

import "time"

type Request struct {
	Kind, URL, TranscriptID string
}

type ReadError struct {
	Code       string
	HTTPStatus int
}

func (e *ReadError) Error() string {
	return "sharepoint_content: " + e.Code
}

type SourceAccount struct {
	Host, WebID, LoginName string
	ID                     int64
}

type NativeFile struct {
	SiteID, WebID, FileID, Path string
	DriveID, ItemID, Title      *string
	ListItemID, Length          *int64
	ModifiedRaw                 *string
	ModifiedAt                  *time.Time
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Kind, State   string
	HTTPStatus    int
	SourceAccount SourceAccount
	File          NativeFile
	Partial       bool
	Losses        []Loss
	PageControls  []PageControl
	Transcript    *Transcript
}

type Transcript struct {
	ID      string
	Entries []TranscriptEntry
}

type TranscriptEntry struct {
	Ordinal            int
	ID, Text           string
	SpeakerDisplayName *string
	StartRaw, EndRaw   *string
	StartMS, EndMS     *int64
}

type PageControl struct {
	Ordinal int
	ID      *string
	Type    *int64
	State   string
	Texts   []string
}

type scriptResult struct {
	State        string        `json:"state"`
	HTTPStatus   int           `json:"status"`
	Account      SourceAccount `json:"account"`
	File         NativeFile    `json:"file"`
	Losses       []Loss        `json:"losses"`
	PageControls []PageControl `json:"pageControls"`
	Transcript   *Transcript   `json:"transcript"`
}

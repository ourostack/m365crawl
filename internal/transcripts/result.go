package transcripts

import "time"

// Entry is one utterance of a transcript. StartMS and EndMS are offsets from the part's start in
// milliseconds; nil when the source gave no readable offset.
type Entry struct {
	Speaker        string
	StartMS, EndMS *int64
	Text           string
}

// The states of a fetch of one part.
const (
	StateOK           = "ok"
	StateNoAccess     = "no_access"
	StateNotFound     = "not_found"
	StateNoTranscript = "no_transcript"
	StateTooLarge     = "too_large"
	StateFailed       = "failed"
)

// The states of a part that no fetch produced.
const (
	// StateNotFetched is a part a fetch can ask for and has not yet.
	StateNotFetched = "not_fetched"
	// StateUnfetchable is a part with no reference a fetch can use.
	StateUnfetchable = "unfetchable"
)

// FetchResult is the outcome of fetching one part. State is one of the fetch states above; a part
// whose fetch met a sign-in page is never stored, so it stays not fetched.
type FetchResult struct {
	State      string
	HTTPStatus int
	Entries    []Entry
	Browser    string
	At         time.Time
}

// PartState is the state of a part as a reader sees it: ok when its text is in the archive,
// unfetchable when no fetch can ask for it, the state of the last attempt when that attempt
// failed, and not_fetched otherwise. hasText says the archive holds the part's text; fetchState is
// the stored state of the last attempt, empty when there was none.
func PartState(p Part, fetchState string, hasText bool) string {
	switch {
	case hasText:
		return StateOK
	case !p.Fetchable():
		return StateUnfetchable
	case fetchState == "" || fetchState == StateOK:
		return StateNotFetched
	}
	return fetchState
}

// Reason says in one plain sentence why a part in the given PartState has no text; empty for ok.
func Reason(p Part, state string) string {
	switch state {
	case StateOK:
		return ""
	case StateUnfetchable:
		switch p.RefQuality {
		case RefShareOnly:
			return "only a sharing link is cached for this part; m365crawl cannot fetch it from ids"
		case RefAMSOnly:
			return "only a Teams media-service link is cached; it is not a SharePoint file"
		case RefUnresolved:
			return "Teams posted a transcript notice but no file reference"
		}
		return "the cached file reference is malformed"
	case StateNoAccess:
		return "SharePoint refused access (HTTP 403); you may have lost access to this file"
	case StateNotFound:
		return "the file is gone (HTTP 404)"
	case StateNoTranscript:
		return "the file has no transcript"
	case StateTooLarge:
		return "the transcript is larger than 32 MiB"
	case StateNotFetched:
		return "not fetched yet; run m365crawl transcripts fetch " + p.CallID
	}
	return "the last fetch attempt failed"
}

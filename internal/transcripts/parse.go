package transcripts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrUnparsable is a recording notice whose XML cannot be read. It carries no detail on purpose:
// the decoder's own message can quote the notice.
var ErrUnparsable = errors.New("the recording notice cannot be parsed")

// transcriptURI is the shape of the SharePoint transcript reference of a part: host, site root,
// drive id, item id and transcript id.
var transcriptURI = regexp.MustCompile(`^https://([^/]+)(/(?:teams|sites|personal)/[^/]+)/_api/v2\.1/drives/([^/]+)/items/([^/]+)/versions/current/media/transcripts/([^/]+)/content$`)

// ParseRecording reads the recording notice of one chat message. ok is false for a notice whose
// status is not Success (such a notice carries no link) and for a message with no notice markup.
// err is ErrUnparsable when the markup cannot be read, or names no call.
func ParseRecording(messageID, threadID, contentHTML string, sentAt time.Time) (p Part, ok bool, err error) {
	at := strings.Index(contentHTML, "<URIObject")
	if at < 0 {
		return Part{}, false, nil
	}
	n, err := readNotice(contentHTML[at:])
	if err != nil {
		return Part{}, false, err
	}
	if n.status != "Success" {
		return Part{}, false, nil
	}
	if n.ids["callId"] == "" {
		return Part{}, false, ErrUnparsable
	}
	p = Part{
		CallID: n.ids["callId"], ThreadID: threadID, MessageID: messageID, SentAt: sentAt,
		StartsAt: parseStamp(n.timestamp), DurationSeconds: parseDuration(n.duration),
		ContentTypes: n.contentTypes, ChunkIndex: n.ids["chunkIndex"],
		StorageKind: n.storage, MeetingICalUID: n.icalUID, OriginalName: n.originalName,
		ShareURL: n.href,
	}
	p.TranscribeOnly = p.ChunkIndex == "10001" || !strings.Contains(p.ContentTypes, "Recording")
	if p.ShareURL == "" {
		p.ShareURL = n.items["onedriveForBusinessVideo"]
	}
	switch m := transcriptURI.FindStringSubmatch(n.items["onedriveForBusinessTranscript"]); {
	case m != nil:
		p.Host, p.SiteRoot, p.DriveID, p.ItemID, p.TranscriptID = m[1], m[2], m[3], m[4], m[5]
		p.RefQuality, p.PartKey = RefDriveItem, "d:"+p.DriveID+"/"+p.ItemID
	case p.ShareURL != "":
		sum := sha256.Sum256([]byte(p.ShareURL))
		p.RefQuality, p.PartKey = RefShareOnly, "u:"+hex.EncodeToString(sum[:])[:16]
	default:
		p.RefQuality, p.PartKey = RefAMSOnly, "m:"+messageID
	}
	return p, true, nil
}

// notice is what a recording notice says, as read from its markup.
type notice struct {
	status, contentTypes, timestamp, duration string
	href, originalName, storage, icalUID      string
	ids, items                                map[string]string
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// readNotice walks the URIObject element. Identifiers and content items are read wherever they
// sit under their parent, so a notice with one more level of nesting still reads.
func readNotice(markup string) (notice, error) {
	n := notice{ids: map[string]string{}, items: map[string]string{}}
	d := xml.NewDecoder(strings.NewReader(markup))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	depth, inIDs, inContent := 0, false, false
	for {
		tok, err := d.Token()
		if err != nil {
			return notice{}, ErrUnparsable // the end of the text before the element closed, or bad markup
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "Identifiers":
				inIDs = true
			case "Id":
				if inIDs {
					n.ids[attr(t, "type")] = attr(t, "value")
				}
			case "RecordingStatus":
				n.status = attr(t, "status")
			case "RecordingContent":
				inContent = true
				n.contentTypes, n.timestamp, n.duration = attr(t, "contentTypes"), attr(t, "timestamp"), attr(t, "duration")
			case "item":
				if inContent {
					n.items[attr(t, "type")] = attr(t, "uri")
				}
			case "a":
				if n.href == "" {
					n.href = attr(t, "href")
				}
			case "OriginalName":
				n.originalName = attr(t, "v")
			case "RecordingStorage":
				n.storage = attr(t, "type")
			case "MeetingICalUid":
				n.icalUID = attr(t, "value")
			}
		case xml.EndElement:
			depth--
			switch t.Name.Local {
			case "Identifiers":
				inIDs = false
			case "RecordingContent":
				inContent = false
			}
			if depth == 0 {
				return n, nil
			}
		}
	}
}

// parseStamp reads the part's start time; a value that is not a time is the zero time.
func parseStamp(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// parseDuration reads h:mm:ss.fff as seconds; anything else is 0.
func parseDuration(s string) float64 {
	f := strings.Split(s, ":")
	if len(f) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(f[0])
	m, err2 := strconv.Atoi(f[1])
	sec, err3 := strconv.ParseFloat(f[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || sec < 0 {
		return 0
	}
	return float64(h*3600+m*60) + sec
}

// ParseTranscriptNotice reads the call id of a transcript notice. The cache stores the notice as
// JSON with backslash-escaped quotes, which is not valid JSON until they are unescaped.
func ParseTranscriptNotice(contentHTML string) (callID string, ok bool) {
	var doc struct {
		CallID string `json:"callId"`
	}
	if err := json.Unmarshal([]byte(strings.ReplaceAll(contentHTML, `\"`, `"`)), &doc); err != nil {
		return "", false
	}
	return doc.CallID, doc.CallID != ""
}

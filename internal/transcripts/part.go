// Package transcripts turns the meeting recording notices a sync already archived into transcript
// parts: one row per recorded part of a call, with the SharePoint reference a later fetch needs.
// Nothing here touches the network.
package transcripts

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// The chat message types a part listing reads.
const (
	TypeRecording  = "RichText/Media_CallRecording"
	TypeTranscript = "RichText/Media_CallTranscript"
)

// RefQuality values: what kind of reference a part carries.
const (
	// RefDriveItem is a part with its own drive id and item id: it can be fetched.
	RefDriveItem = "drive_item"
	// RefShareOnly is a part with only a path or sharing link.
	RefShareOnly = "share_only"
	// RefAMSOnly is a part with only a link to Teams' own media service.
	RefAMSOnly = "ams_only"
	// RefUnresolved stands for a call that has a transcript notice and no part with a transcript.
	RefUnresolved = "unresolved"
)

// PartsMapper numbers how the notices are turned into parts. It is stored as
// meta.transcript_parts_mapper; raise it when ParseRecording or Assemble changes what they derive,
// and the next sync rebuilds the parts of every account.
const PartsMapper = 1

// Part is one recorded part of a call, as the Success notice of its recording describes it.
type Part struct {
	AccountID, CallID, ThreadID, MessageID, PartKey string
	// Ordinal is the part's place in its call, from 1, in time order.
	Ordinal         int
	StartsAt        time.Time
	DurationSeconds float64
	ContentTypes    string
	ChunkIndex      string
	// TranscribeOnly is a part that was transcribed and not recorded.
	TranscribeOnly                                             bool
	Host, SiteRoot, StorageKind, DriveID, ItemID, TranscriptID string
	ShareURL, RefQuality, MeetingICalUID, OriginalName         string
	SentAt                                                     time.Time
}

// Notice is a transcript notice of a call: the message that says a transcript exists.
type Notice struct {
	ThreadID, MessageID string
	SentAt              time.Time
}

var (
	refHost     = regexp.MustCompile(`^[a-z0-9.-]+$`)
	refSiteRoot = regexp.MustCompile(`^/(teams|sites|personal)/[^/?#]+$`)
	refID       = regexp.MustCompile(`^[A-Za-z0-9!_.%-]+$`)
)

// sharePointSuffixes are the domains SharePoint Online serves its sites from. A fetch opens a
// signed-in browser on the part's host, so no other host is ever fetched.
var sharePointSuffixes = []string{".sharepoint.com", ".sharepoint.us", ".sharepoint-mil.us", ".sharepoint.cn", ".sharepoint.de"}

// allowedTestSuffix is one more suffix a test allows, for the synthetic hosts of its fixtures.
var allowedTestSuffix string

// inTestBinary reports whether this program is a test binary; a variable so a test can say no.
var inTestBinary = testing.Testing

// AllowHostSuffixForTests lets tests use synthetic hosts ending in suffix (under the reserved
// .invalid domain, which never resolves) as SharePoint hosts. Only tests call it; an empty suffix
// allows none. It returns a function that puts back the previous suffix. Outside a test binary
// it panics, so the allowance can never reach a real run.
func AllowHostSuffixForTests(suffix string) (restore func()) {
	if !inTestBinary() {
		panic("AllowHostSuffixForTests outside a test binary")
	}
	old := allowedTestSuffix
	allowedTestSuffix = suffix
	return func() { allowedTestSuffix = old }
}

// IsSharePointHost reports whether host is a plain host name under one of SharePoint's domains,
// ignoring case.
func IsSharePointHost(host string) bool {
	h := strings.ToLower(host)
	if !refHost.MatchString(h) {
		return false
	}
	suffixes := sharePointSuffixes
	if allowedTestSuffix != "" {
		suffixes = append(suffixes[:len(suffixes):len(suffixes)], allowedTestSuffix)
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	for _, s := range suffixes {
		if len(h) > len(s) && strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// ValidRef reports whether the part's file reference is safe to build a request from: a SharePoint
// host name, a site root of one known kind, and ids made only of the characters such ids use.
func (p Part) ValidRef() bool {
	return IsSharePointHost(p.Host) && refSiteRoot.MatchString(p.SiteRoot) &&
		refID.MatchString(p.DriveID) && refID.MatchString(p.ItemID) && refID.MatchString(p.TranscriptID)
}

// Fetchable reports whether a fetch can ask SharePoint for this part.
func (p Part) Fetchable() bool { return p.RefQuality == RefDriveItem && p.ValidRef() }

// HasTranscript reports whether the part's notice says it carries a transcript.
func (p Part) HasTranscript() bool { return strings.Contains(p.ContentTypes, "Transcript") }

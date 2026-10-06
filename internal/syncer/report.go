package syncer

import (
	"fmt"
	"time"

	"github.com/ourostack/teamscrawl/internal/store"
)

// Report statuses.
const (
	StatusOK        = "ok"
	StatusOmissions = "ok_with_omissions"
	StatusUnchanged = "unchanged"
	// StatusPartial is a run in which at least one source committed and at least one failed (or the run
	// was interrupted after one committed); StatusFailed is a run in which none did.
	StatusPartial = "partial"
	StatusFailed  = "failed"
	kindMessage   = "message"
	kindActivity  = "activity"
	testPauseEnv  = "TEAMSCRAWL_TEST_PAUSE_AFTER_SNAPSHOT"
	// testPauseStubbornEnv=1 makes that pause ignore cancellation (the e2e test of the forced exit).
	testPauseStubbornEnv = "TEAMSCRAWL_TEST_PAUSE_IGNORES_CANCEL"
	// testPauseMarker is the stderr line printed when the test pause starts (e2e tests wait for it).
	testPauseMarker    = "teamscrawl-test: paused after snapshot"
	staleSnapshotAfter = time.Hour
)

// Report is what one sync did. Counts add up over the sources that committed.
type Report struct {
	Status        string         `json:"status"` // ok | ok_with_omissions | unchanged | partial | failed
	Sources       []SourceReport `json:"sources"`
	Conversations store.Counts   `json:"conversations"`
	Messages      store.Counts   `json:"messages"`
	People        store.Counts   `json:"people"`
	Activity      store.Counts   `json:"activity"`
	Records       store.Counts   `json:"records"`
	// Calendar is what filling the calendar tables from the calendar and recap records did.
	Calendar  store.CalendarCounts `json:"calendar"`
	Omissions map[string]int       `json:"omissions"`
	// Redacted counts the credential-looking fragments removed from generic records before they
	// were archived (JWTs, token fields, signed-URL signatures). It is a count, not an omission.
	Redacted     int      `json:"redacted"`
	OtherOrigins []string `json:"other_origins"`
	// Migrated is set when this run first recomputed an older archive's derived fields from their
	// stored raw_json (see store.DerivationVersion). Those rows count as no update and no edit.
	Migrated   *store.Migration `json:"migrated,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt time.Time        `json:"finished_at"`
}

// SourceReport is one Teams origin's outcome.
type SourceReport struct {
	Source    string         `json:"source"` // "<profile>|<origin>"
	Status    string         `json:"status"` // ok | ok_with_omissions | unchanged | failed
	Omissions map[string]int `json:"omissions,omitempty"`
	Redacted  int            `json:"redacted,omitempty"`
	// Accounts and Counts describe a source that was decoded and committed; Error is set when the
	// source failed (its rows were rolled back).
	Accounts []string      `json:"accounts,omitempty"` // "<tenantId>/<userId>"
	Counts   *SourceCounts `json:"counts,omitempty"`
	Error    *SourceError  `json:"error,omitempty"`
	// NextReadAfter is set on an Outlook source that was skipped_interval: the earliest time it is read again.
	NextReadAfter *time.Time `json:"next_read_after,omitempty"`
}

// SourceCounts is what one source's commit did.
type SourceCounts struct {
	Conversations store.Counts `json:"conversations"`
	Messages      store.Counts `json:"messages"`
	People        store.Counts `json:"people"`
	Activity      store.Counts `json:"activity"`
	Records       store.Counts `json:"records"`
	// Calendar is what the calendar derivation did in the source's transaction.
	Calendar store.CalendarCounts `json:"calendar"`
}

// SourceError is why a source failed: an error code from the output contract and its message.
type SourceError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Change is one message or activity item the sync added, edited or deleted, taken from what the
// store's Apply calls report. Key is the store's row key: "<tenant>|<user>|<conversation>|<id>"
// for a message, "<tenant>|<user>|<id>" for an activity item.
type Change struct {
	Kind   string `json:"kind"`   // message | activity
	Change string `json:"change"` // new | edited | deleted
	Key    string `json:"key"`
}

func add(a *store.Counts, b store.Counts) {
	a.Seen += b.Seen
	a.Inserted += b.Inserted
	a.Updated += b.Updated
	a.Unchanged += b.Unchanged
}

// lost is how many records a sync could not read: the omissions that mean data was missed. The
// denied_database and denied_store counts are the credential denylist working as designed, not
// loss, and an event in an unknown time zone or an all-day event that fits no whole-day shape is
// stored and shown, only less precisely, so none of these makes a sync ok_with_omissions.
func lost(m map[string]int) int {
	n := 0
	for k, v := range m {
		switch k {
		case "denied_database", "denied_store", store.OmitCalendarUnknownZone, store.OmitCalendarAllDayUnaligned:
			continue
		}
		n += v
	}
	return n
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

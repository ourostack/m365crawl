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
	statusFailed    = "failed"
	kindMessage     = "message"
	kindActivity    = "activity"
	testPauseEnv    = "TEAMSCRAWL_TEST_PAUSE_AFTER_SNAPSHOT"
	// testPauseMarker is the stderr line printed when the test pause starts (e2e tests wait for it).
	testPauseMarker    = "teamscrawl-test: paused after snapshot"
	staleSnapshotAfter = time.Hour
)

// Report is what one successful sync did. Counts add up over all sources.
type Report struct {
	Status        string         `json:"status"` // ok | ok_with_omissions | unchanged
	Sources       []SourceReport `json:"sources"`
	Conversations store.Counts   `json:"conversations"`
	Messages      store.Counts   `json:"messages"`
	People        store.Counts   `json:"people"`
	Activity      store.Counts   `json:"activity"`
	Omissions     map[string]int `json:"omissions"`
	OtherOrigins  []string       `json:"other_origins"`
	// Migrated is set when this run first recomputed an older archive's derived fields from their
	// stored raw_json (see store.DerivationVersion). Those rows count as no update and no edit.
	Migrated   *store.Migration `json:"migrated,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt time.Time        `json:"finished_at"`
}

// SourceReport is one Teams origin's outcome.
type SourceReport struct {
	Source    string         `json:"source"` // "<profile>|<origin>"
	Status    string         `json:"status"`
	Omissions map[string]int `json:"omissions,omitempty"`
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

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

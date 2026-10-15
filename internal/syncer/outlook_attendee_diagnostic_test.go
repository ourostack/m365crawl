package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
)

func TestAttendeeDiagnosticWriteFailureIsPropagated(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	old := attendeeCollect
	t.Cleanup(func() { attendeeCollect = old })
	attendeeCollect = func(context.Context, outlookdesktop.Info, *outlookcal.AttendeeSelections) (outlookmeetingattendees.Evidence, outlookmeetingattendees.Census, string, error) {
		return outlookmeetingattendees.Evidence{}, outlookmeetingattendees.Census{}, "", errors.New("synthetic reader failure")
	}
	raw := openRaw(t, db)
	// Open the writer once so the failure trigger belongs to the synthetic archive.
	s, err := peopleOpen(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`create trigger fail_native_diagnostic before update on knowledge_attempts when new.outcome='failed' begin select raise(abort,'synthetic diagnostic'); end`); err != nil {
		t.Fatal(err)
	}
	_, err = SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	var sqlError interface{ Code() int }
	if !errors.As(err, &sqlError) {
		t.Fatal("diagnostic SQLite failure disappeared", err)
	}
	if count(t, db, `select count(*) from outlook_attendee_members`) != 0 {
		t.Fatal("failed diagnostic admitted native")
	}
}

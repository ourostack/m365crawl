package syncer

import (
	"context"
	"testing"
)

func TestAttendeeSharedBeginFailureIsIndependent(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	if _, err := SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	peopleSQL(t, db, `update outlook_attendee_requests set request_json='{}'`)
	opts := outlookOpts(db, root)
	opts.FullRead = true
	rep, _ := run(t, opts)
	if got := sourceKeyed(t, rep, "outlook|Main|attendees"); got.Status != "failed" || got.Error == nil {
		t.Fatal("native begin error missing", got)
	}
	if got := sourceKeyed(t, rep, "outlook|Main"); got.Status == StatusFailed {
		t.Fatal("optional registration failure poisoned ordinary source", got)
	}
	if count(t, db, `select count(*) from outlook_attendee_members`) != 1 {
		t.Fatal("invalid request acquired native evidence")
	}
}

package store

import (
	"context"
	"testing"
	"time"
)

// A read marker says the account's mail was read by a sync; a failure clears it and is kept; a
// later read forgets the failure.
func TestMailStateMarkerAndFailure(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	st, err := s.MailState(ctx, mailAcct)
	if err != nil || st.Read || st.Failure != nil || !st.ReadAt.IsZero() {
		t.Fatalf("fresh archive: %+v %v", st, err)
	}
	if err := s.SetMailRead(ctx, mailAcct, mailT0); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.MailState(ctx, mailAcct); !st.Read || !st.ReadAt.Equal(mailT0) {
		t.Fatalf("after read: %+v", st)
	}
	if err := s.SetMailFailure(ctx, mailAcct, OutlookFailure{Code: "c", Message: "m", Fix: "f", Exit: 3}); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.MailState(ctx, mailAcct); st.Read || st.Failure == nil || st.Failure.Code != "c" {
		t.Fatalf("after failure: %+v", st)
	}
	if err := s.SetMailRead(ctx, mailAcct, mailT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.MailState(ctx, mailAcct); !st.Read || st.Failure != nil {
		t.Fatalf("after second read: %+v", st)
	}
	other, _ := s.MailState(ctx, "outlook/other")
	if other.Read {
		t.Fatal("marker leaked to another account")
	}
}

func TestMailStateBadFailureJSON(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.db.Exec(`insert into meta(key, value) values('outlook_mail_failure:` + mailAcct + `', 'not json')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MailState(ctx, mailAcct); err == nil {
		t.Fatal("bad failure json accepted")
	}
}

func TestMailStateErrorsOnClosedArchive(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_ = s.db.Close()
	if _, err := s.MailState(ctx, mailAcct); err == nil {
		t.Error("MailState")
	}
	if err := s.SetMailRead(ctx, mailAcct, mailT0); err == nil {
		t.Error("SetMailRead")
	}
	if err := s.SetMailFailure(ctx, mailAcct, OutlookFailure{}); err == nil {
		t.Error("SetMailFailure")
	}
	if _, err := s.MailStatus(ctx); err == nil {
		t.Error("MailStatus")
	}
}

// The status block counts live messages only and reports the newest read marker.
func TestMailStatus(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	empty, err := s.MailStatus(ctx)
	if err != nil || empty.Messages != 0 || !empty.OldestAt.IsZero() || !empty.SyncedAt.IsZero() {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	read := false
	a, b, c := mailMsg(1, fInbox, "<1@x>", "One", 30), mailMsg(2, fInbox, "<2@x>", "Two", 12), mailMsg(3, fInbox, "<3@x>", "Three", 2)
	b.Unread, b.ReadState = &read, "read"
	mustCommitMail(t, s, mailBatch(mailT0, a, b, c))
	if _, err := s.db.Exec(`update mail_messages set gone_at='2026-09-10T08:00:00.000Z' where detail_key=3`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMailRead(ctx, mailAcct, mailT0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMailRead(ctx, "outlook/other", mailT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.MailStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages != 2 || got.Unread != 1 || got.Folders != 5 || !got.OldestAt.Equal(mailT0.AddDate(0, 0, -30)) || !got.SyncedAt.Equal(mailT0.Add(time.Hour)) {
		t.Fatalf("status: %+v", got)
	}
}

func TestMailStateWriteFailuresRollBack(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		sweepFaults(t, nil, func(ctx context.Context, s *Store) error { return s.SetMailRead(ctx, mailAcct, mailT0) })
	})
	t.Run("failure", func(t *testing.T) {
		sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
			return s.SetMailFailure(ctx, mailAcct, OutlookFailure{Code: "c"})
		})
	})
}

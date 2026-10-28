package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func TestWorkIQMailInitialNativeSnapshotPrecedesServerReduction(t *testing.T) {
	for _, nativeBody := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeBody), func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			local := mailMsg(100, fInbox, "<ONE@example.test>", "Subject", 0)
			if !nativeBody {
				local.Body.HTML = nil
			}
			nativeReadAt := mailT0.Add(time.Minute)
			mustCommitMail(t, s, mailBatch(nativeReadAt, local))
			prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
			_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
			var projection, seen sql.NullString
			must0(s.db.QueryRowContext(ctx, `select cache_projection,cache_seen_at from workiq_rows where kind='mail'`).Scan(&projection, &seen))
			if !projection.Valid || !seen.Valid {
				t.Fatal("first server arrival did not snapshot the original native source and observation")
			}
			var snapshot outlookmail.Message
			must0(json.Unmarshal([]byte(projection.String), &snapshot))
			if snapshot.Account != mailAcct || snapshot.DetailKey != 100 || snapshot.MessageID != local.MessageID ||
				snapshot.Subject != local.Subject || snapshot.SenderAddress != local.SenderAddress || !snapshot.Sent.Equal(local.Sent) ||
				!bytes.Equal(snapshot.Body.HTML, local.Body.HTML) {
				t.Fatal("native source snapshot contains server data or lost its own immutable identity/body")
			}
			clock := must(workIQStoredTime(seen))
			if clock == nil || !clock.Equal(nativeReadAt) {
				t.Fatal("snapshot fabricated a current native sighting instead of retaining the actual old observation")
			}
			item := must(s.MailGet(ctx, mailAcct, 100))
			want := "Server body"
			if nativeBody {
				want = "body of Subject"
			}
			if item.BodyText != want || item.InternetMessageID != local.MessageID || rowCount(t, s, `select count(*) from mail_messages`) != 1 {
				t.Fatal("snapshot admission changed the first public identity or chose the wrong source body")
			}
		})
	}
}

func TestWorkIQMailEmptyNativeArrivalRetainsKnownCacheBody(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	local := mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)
	mustCommitMail(t, s, mailBatch(mailT0, local))
	var key uint32
	must0(s.db.QueryRowContext(ctx, `select detail_key from mail_messages`).Scan(&key))
	local.Body.HTML = nil
	local.Body.State = outlookmail.BodyMissing
	mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), local))
	item := must(s.MailGet(ctx, mailAcct, key))
	if item.BodyText != "body of Subject" {
		t.Fatal("a later empty native capture replaced known cache text with conflicting server text")
	}
	var projection, seen string
	must0(s.db.QueryRowContext(ctx, `select cache_projection,cache_seen_at from workiq_rows where kind='mail'`).Scan(&projection, &seen))
	var snapshot outlookmail.Message
	must0(json.Unmarshal([]byte(projection), &snapshot))
	if len(snapshot.Body.HTML) == 0 {
		t.Fatal("empty capture erased the retained nonempty native body")
	}
	clock := must(workIQStoredTime(sql.NullString{String: seen, Valid: true}))
	if clock == nil || !clock.Equal(mailT0.Add(time.Hour)) {
		t.Fatal("retained content prevented the actual new native presence from being recorded")
	}
}

func TestWorkIQMailNativeSnapshotPopulatedFaultsAndLegacyText(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		local := mailMsg(100, fInbox, "one@example.test", "Subject", 0)
		local.Attachments = []outlookmail.Attachment{{Key: 11, MessageKey: 100, Name: "native.txt", ContentType: "text/plain", Size: 9}}
		mustCommitMail(t, s, mailBatch(mailT0, local))
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		return s.inTx(ctx, func(tx *sql.Tx) error {
			var id int64
			if err := tx.QueryRowContext(ctx, `select rowid from mail_messages where detail_key=100`).Scan(&id); err != nil {
				return err
			}
			_, _, err := snapshotWorkIQNativeMail(ctx, tx, mailAcct, id)
			return err
		})
	})
	s := newStore(t)
	setup(t, s)
	_, err := s.db.Exec(`update mail_messages set body_html_gz=null,is_read=null,received_at=null,sent_at=null`)
	must0(err)
	must0(s.inTx(context.Background(), func(tx *sql.Tx) error {
		var id int64
		must0(tx.QueryRow(`select rowid from mail_messages`).Scan(&id))
		raw, seen, err := snapshotWorkIQNativeMail(context.Background(), tx, mailAcct, id)
		if err != nil {
			return err
		}
		var snapshot outlookmail.Message
		must0(json.Unmarshal(raw, &snapshot))
		if !bytes.Contains(snapshot.Body.HTML, []byte("body of Subject")) || snapshot.Unread != nil || !snapshot.Sent.IsZero() ||
			len(snapshot.Attachments) != 1 || snapshot.Attachments[0].Name != "native.txt" || seen == nil || !seen.Equal(mailT0) {
			t.Fatal("legacy known plaintext/nullable clocks or native child metadata were lost or fabricated")
		}
		return nil
	}))
}

func TestWorkIQMailNativeSnapshotRejectsInvalidScrubAndClocks(t *testing.T) {
	local := mailMsg(100, fInbox, "one@example.test", "Subject", 0)
	invalid := local
	invalid.Sent = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := cleanWorkIQNativeMail(invalid); err == nil {
		t.Fatal("unencodable native clock was retained")
	}
	original := scrubWorkIQProjection
	t.Cleanup(func() { scrubWorkIQProjection = original })
	scrubWorkIQProjection = func([]byte) ([]byte, int) { return []byte(`{`), 0 }
	if _, _, err := cleanWorkIQNativeMail(local); err == nil {
		t.Fatal("invalid scrubbed body was retained")
	}
	scrubWorkIQProjection = func(raw []byte) ([]byte, int) {
		if len(raw) > 0 && raw[0] == '{' {
			return []byte(`{`), 0
		}
		return original(raw)
	}
	if _, _, err := cleanWorkIQNativeMail(local); err == nil {
		t.Fatal("invalid scrubbed native identity projection was retained")
	}
	scrubWorkIQProjection = original
	for _, column := range []string{"sent_at", "state_seen_at"} {
		t.Run(column, func(t *testing.T) {
			s := newStore(t)
			mustCommitMail(t, s, mailBatch(mailT0, local))
			_, err := s.db.Exec(`update mail_messages set ` + column + `='bad'`)
			must0(err)
			must0(s.inTx(context.Background(), func(tx *sql.Tx) error {
				var id int64
				must0(tx.QueryRow(`select rowid from mail_messages`).Scan(&id))
				if _, _, err := snapshotWorkIQNativeMail(context.Background(), tx, mailAcct, id); err == nil {
					t.Fatal("corrupt original native clock was snapshotted")
				}
				return nil
			}))
		})
	}
}

func TestWorkIQMailUnchangedNativeAfterSnapshotDoesNotInventPublicEdit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	local := mailMsg(100, fInbox, "one@example.test", "Subject", 0)
	mustCommitMail(t, s, mailBatch(mailT0, local))
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	var before string
	must0(s.db.QueryRowContext(ctx, `select modified_at from mail_messages`).Scan(&before))
	result := must(s.CommitMail(ctx, mailBatch(mailT0.Add(time.Hour), local)))
	var after string
	must0(s.db.QueryRowContext(ctx, `select modified_at from mail_messages`).Scan(&after))
	if result.Updated != 0 || before != after {
		t.Fatal("materializing private native source fields invented a canonical content edit")
	}
	var seen string
	must0(s.db.QueryRowContext(ctx, `select cache_seen_at from workiq_rows where kind='mail'`).Scan(&seen))
	clock := must(workIQStoredTime(sql.NullString{String: seen, Valid: true}))
	if clock == nil || !clock.Equal(mailT0.Add(time.Hour)) {
		t.Fatal("unchanged actual native presence lost its independent sighting")
	}
}

func TestWorkIQMailNativeCopyClockFaultsAndUnknowns(t *testing.T) {
	sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
		_, err := s.CommitMail(ctx, mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0)))
		return err
	})
	for _, bad := range []string{"bad", "3000-01-01T00:00:00Z"} {
		t.Run(bad, func(t *testing.T) {
			s := newStore(t)
			_, err := s.db.Exec(`insert into meta(key,value) values(?,?)`, workIQMailFreshPrefix+mailAcct, bad)
			must0(err)
			before := dump(t, s)
			if _, err := s.CommitMail(context.Background(), mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0))); err == nil || dump(t, s) != before {
				t.Fatal("corrupt retained native copy clock was accepted or partially changed data")
			}
		})
	}
	s := newStore(t)
	b := mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0))
	b.FreshAt = time.Time{}
	mustCommitMail(t, s, b)
	if rowCount(t, s, `select count(*) from meta where key=?`, workIQMailFreshPrefix+mailAcct) != 0 {
		t.Fatal("unknown native copy clock became evidence")
	}
	b.FreshAt = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	before := dump(t, s)
	if _, err := s.CommitMail(context.Background(), b); err == nil || dump(t, s) != before {
		t.Fatal("overflowing native copy clock was silently wrapped")
	}
	b.FreshAt = mailT0
	mustCommitMail(t, s, b)
	b.FreshAt = mailT0.Add(-time.Hour)
	mustCommitMail(t, s, b)
	var fresh string
	must0(s.db.QueryRow(`select value from meta where key=?`, workIQMailFreshPrefix+mailAcct).Scan(&fresh))
	if fresh != workIQTimeValue(&mailT0) {
		t.Fatal("older native copy moved the retained high-water mark backwards")
	}
	bound := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, bound)
	prepareRealMailInventory(t, bound, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(bound.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	b.FreshAt = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	before = dump(t, bound)
	if _, err := bound.CommitMail(ctx, b); err == nil || dump(t, bound) != before {
		t.Fatal("bound source converted an overflowing native clock into a valid version")
	}
}

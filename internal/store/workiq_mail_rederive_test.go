package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestWorkIQMailRebuildUsesRetainedServerWithoutNewSourceEvidence(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	const facts = `select json_object('graph',graph_id,'server',server_json,'projection',server_projection,'cache',cache_projection,
'seen',server_seen_at,'cache_seen',cache_seen_at,'modified',modified_at,'deleted',server_deleted_at,'gone',server_gone_at)
from workiq_rows where kind='mail'`
	var before string
	must0(s.db.QueryRowContext(ctx, facts).Scan(&before))
	var id int64
	var key uint32
	must0(s.db.QueryRowContext(ctx, `select rowid,detail_key from mail_messages`).Scan(&id, &key))
	_, err := s.db.ExecContext(ctx, `update mail_messages set body_text='obsolete',body_html_gz=null,body_text_version=0,subject_norm='obsolete'`)
	must0(err)
	_, err = s.db.ExecContext(ctx, `insert into meta(key,value) values('mail_text_version','0') on conflict(key) do update set value='0'`)
	must0(err)
	_, err = s.RebuildWorkIQMail(ctx)
	must0(err)
	item := must(s.MailGet(ctx, mailAcct, key))
	var version int
	must0(s.db.QueryRowContext(ctx, `select body_text_version from mail_messages where rowid=?`, id).Scan(&version))
	if item.BodyText != "Server body" || version != MailTextVersion ||
		rowCount(t, s, `select count(*) from mail_fts where mail_fts match 'Server'`) != 1 {
		t.Fatal("rebuild did not regenerate canonical text/version/FTS from retained server projection")
	}
	var after string
	must0(s.db.QueryRowContext(ctx, facts).Scan(&after))
	if before != after || rowCount(t, s, `select count(*) from mail_messages`) != 1 {
		t.Fatal("source-only rebuild changed raw source facts, clocks or stable row identity")
	}
}

func TestWorkIQMailSourceRebuildFaultsKeepNativeAndServerFacts(t *testing.T) {
	for _, cache := range []bool{false, true} {
		t.Run(fmt.Sprint(cache), func(t *testing.T) {
			setup := func(t *testing.T, s *Store) {
				ctx := context.Background()
				attempt := seedWorkIQMailAttempt(t, s)
				if cache {
					mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0)))
				}
				prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
				_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
				_, err := s.db.ExecContext(ctx, `update mail_messages set body_text_version=0,body_text='obsolete',body_html_gz=null`)
				must0(err)
				_, err = s.db.ExecContext(ctx, `insert into meta(key,value) values('mail_text_version','0') on conflict(key) do update set value='0'`)
				must0(err)
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error { _, err := s.RebuildWorkIQMail(ctx); return err })
			s := newStore(t)
			setup(t, s)
			_, err := s.RebuildWorkIQMail(context.Background())
			must0(err)
			want := "Server body"
			if cache {
				want = "body of Subject"
			}
			var text string
			must0(s.db.QueryRow(`select body_text from mail_messages`).Scan(&text))
			if text != want {
				t.Fatal("source-specific rebuild did not respect the retained content winner")
			}
		})
	}
}

func TestWorkIQMailSourceRebuildRejectsCorruptSourceFacts(t *testing.T) {
	for _, edit := range []string{
		`update workiq_rows set server_projection='{'`,
		`update workiq_rows set server_projection=json_set(server_projection,'$.ID','foreign')`,
		`update workiq_rows set winner='desktop_cache',cache_projection=null`,
		`update workiq_rows set winner='desktop_cache',cache_projection='{'`,
		`update workiq_rows set winner='desktop_cache',cache_projection='{"Account":"foreign","DetailKey":1}'`,
		"driver-winner",
	} {
		t.Run(edit, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
			_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
			_, err := s.db.ExecContext(ctx, `update mail_messages set body_text_version=0`)
			must0(err)
			_, err = s.db.ExecContext(ctx, `insert into meta(key,value) values('mail_text_version','0') on conflict(key) do update set value='0'`)
			must0(err)
			if edit != "driver-winner" {
				_, err = s.db.ExecContext(ctx, edit)
				must0(err)
			}
			before := dump(t, s)
			var inj *injector
			if edit == "driver-winner" {
				inj = injectFaults(t, s)
				inj.arm("mail-winner-corrupt", 1)
			}
			_, err = s.RebuildWorkIQMail(ctx)
			if inj != nil && !inj.disarm() {
				t.Fatal("corrupt source-winner row did not reach the real rebuild boundary")
			}
			if err == nil || dump(t, s) != before {
				t.Fatal("corrupt source facts were rebuilt or partially committed")
			}
		})
	}
}

func TestWorkIQMailProjectionUpgradeIsSeparateFromNativeTextVersion(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	_, err := s.db.ExecContext(ctx, `update mail_messages set body_text='obsolete',body_html_gz=null`)
	must0(err)
	_, err = s.db.ExecContext(ctx, `insert into meta(key,value) values(?, '0') on conflict(key) do update set value='0'`, workIQMailProjectionKey)
	must0(err)
	if changed := must(s.EnsureWorkIQMailProjection(ctx)); changed != 1 {
		t.Fatal("old source projection version did not rebuild its actual retained source")
	}
	var body string
	must0(s.db.QueryRowContext(ctx, `select body_text from mail_messages`).Scan(&body))
	if body != "Server body" {
		t.Fatal("source projection version upgrade did not repair the canonical body")
	}
	before := dump(t, s)
	if changed := must(s.EnsureWorkIQMailProjection(ctx)); changed != 0 || dump(t, s) != before {
		t.Fatal("current source projection version replay fabricated changes")
	}
}

func TestWorkIQMailProjectionVersionFaultsAndBounds(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		ctx := context.Background()
		attempt := seedWorkIQMailAttempt(t, s)
		prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
		_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
		_, err := s.db.ExecContext(ctx, `update meta set value='0' where key=?`, workIQMailProjectionKey)
		must0(err)
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error { _, err := s.EnsureWorkIQMailProjection(ctx); return err })
	for _, version := range []string{"not-a-version", "-1", "2"} {
		t.Run(version, func(t *testing.T) {
			s := newStore(t)
			setup(t, s)
			_, err := s.db.Exec(`update meta set value=? where key=?`, version, workIQMailProjectionKey)
			must0(err)
			before := dump(t, s)
			if _, err := s.EnsureWorkIQMailProjection(context.Background()); err == nil || dump(t, s) != before {
				t.Fatal("invalid/newer source projection version was ignored or changed the archive")
			}
		})
	}
	s := newStore(t)
	before := dump(t, s)
	if changed := must(s.EnsureWorkIQMailProjection(context.Background())); changed != 0 || dump(t, s) != before {
		t.Fatal("absent source projection marker became a source upgrade")
	}
}

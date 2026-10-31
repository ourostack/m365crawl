package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func openStaleWorkIQAttempt(t *testing.T, db string, scope store.WorkIQScope) {
	t.Helper()
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginWorkIQAttempt(context.Background(), scope, workIQNow()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// A run whose completion could not be saved left its attempt open; the next run of the same
// (family, account, kind) closes it, but only while it owns that scope, so an attempt with a live
// owner is never closed. Other scopes neither block the run nor have their attempts closed.
func TestWorkIQNextRunClosesAStaleAttemptOnlyWhileOwningItsScope(t *testing.T) {
	db, mailAccount := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	chatAccount := "synthetic-tenant/synthetic-user"
	options := WorkIQOptions{DBPath: db, Source: "chats", Kind: "coverage", MaxChats: 1}
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	openStaleWorkIQAttempt(t, db, store.WorkIQScope{Family: "chats", Account: chatAccount, Kind: "coverage"})
	openStaleWorkIQAttempt(t, db, store.WorkIQScope{Family: "mail", Account: mailAccount, Kind: "coverage"})
	other, err := store.AcquireLock(workIQRunLockPath(db, "mail", mailAccount, "coverage"))
	if err != nil {
		t.Fatal(err)
	}
	defer other()
	live, err := store.AcquireLock(workIQRunLockPath(db, "chats", chatAccount, "coverage"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunWorkIQ(context.Background(), options)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "locked" || count(t, db, `select count(*) from workiq_attempts where status='open'`) != 2 {
		t.Fatal("a run closed an attempt whose owner may still be running", err)
	}
	live()
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal("another scope's live owner blocked this scope's run", err)
	}
	if count(t, db, `select count(*) from workiq_attempts where status='open' and family='mail' and kind='coverage'`) != 1 ||
		count(t, db, `select count(*) from workiq_attempts where status='open' and family='chats'`) != 0 ||
		count(t, db, `select count(*) from workiq_attempts where error_code='workiq_interrupted' and finished_at is not null`) != 1 {
		t.Fatal("the stale attempt was not closed as interrupted, or another scope's attempt was closed")
	}
	old := workIQCloseStale
	t.Cleanup(func() { workIQCloseStale = old })
	broken := errors.New("synthetic stale-closure failure")
	workIQCloseStale = func(context.Context, string, store.WorkIQScope, time.Time) error { return broken }
	if _, err := RunWorkIQ(context.Background(), options); !errors.Is(err, broken) {
		t.Fatal("a failed stale closure was ignored", err)
	}
	if lock, err := store.AcquireLock(workIQRunLockPath(db, "chats", chatAccount, "coverage")); err != nil {
		t.Fatal("a failed stale closure kept the run lock", err)
	} else {
		lock()
	}
}

// Different accounts, families and kinds get different run locks; one scope always gets the same.
func TestWorkIQRunLockPathNamesItsWholeScope(t *testing.T) {
	base := workIQRunLockPath("archive.db", "mail", "outlook/a", "sync")
	if base != workIQRunLockPath("archive.db", "mail", "outlook/a", "sync") || !strings.HasPrefix(base, "archive.db.workiq-run-") {
		t.Fatal("run lock path is not stable", base)
	}
	for _, other := range []string{
		workIQRunLockPath("archive.db", "mail", "outlook/b", "sync"),
		workIQRunLockPath("archive.db", "chats", "outlook/a", "sync"),
		workIQRunLockPath("archive.db", "mail", "outlook/a", "coverage"),
		workIQRunLockPath("archive.db", "mail", "outlook/a\x00sync", ""),
		workIQRunLockPath("other.db", "mail", "outlook/a", "sync"),
	} {
		if other == base {
			t.Fatal("two scopes share a run lock", other)
		}
	}
}

// A sync of a scope another live run owns is refused before it opens a session, for mail and chats.
func TestWorkIQSyncOfAnOwnedScopeIsLocked(t *testing.T) {
	db, mailAccount := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	for _, tc := range []struct{ family, account string }{{"mail", mailAccount}, {"chats", "synthetic-tenant/synthetic-user"}} {
		live, err := store.AcquireLock(workIQRunLockPath(db, tc.family, tc.account, "sync"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: tc.family, Account: tc.account, MaxChats: 1})
		live()
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Code != "locked" || count(t, db, `select count(*) from workiq_attempts where family='`+tc.family+`'`) != 0 {
			t.Fatal(tc.family, "sync ran while another run owned its scope", err)
		}
	}
}

func TestWorkIQAccountChoicePassesOtherErrorsThrough(t *testing.T) {
	other := errors.New("synthetic selection failure")
	if got := workIQAccountChoice(other, nil); !errors.Is(got, other) {
		t.Fatal("an unrelated selection error was rewritten", got)
	}
}

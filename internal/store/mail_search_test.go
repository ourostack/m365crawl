package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestMailSearchMatchesNewestFirstAndReportsTruncation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0,
		mailMsg(1, fInbox, "<1@x>", "Budget review", 5),
		mailMsg(2, fInbox, "<2@x>", "Holiday photos", 4),
		mailMsg(3, fSent, "<3@x>", "Budget sent", 3),
		mailMsg(4, fInbox, "<4@x>", "Budget again", 2)))
	rows, trunc, err := s.MailSearch(ctx, "budget", MailFilter{})
	if err != nil || trunc || len(rows) != 3 || rows[0].DetailKey != 4 || rows[1].DetailKey != 3 || rows[2].DetailKey != 1 {
		t.Fatalf("budget: %v %v %+v", err, trunc, rows)
	}
	rows, trunc, err = s.MailSearch(ctx, "budget", MailFilter{Limit: 2})
	if err != nil || !trunc || len(rows) != 2 || rows[0].DetailKey != 4 {
		t.Fatalf("limit 2: %v %v %+v", err, trunc, rows)
	}
	rows, trunc, err = s.MailSearch(ctx, "budget", MailFilter{Limit: 3})
	if err != nil || trunc || len(rows) != 3 {
		t.Fatalf("limit 3 is exactly enough: %v %v %d", err, trunc, len(rows))
	}
	// The query argument wins over a Query already on the filter.
	rows, _, err = s.MailSearch(ctx, "holiday", MailFilter{Query: "budget"})
	if err != nil || len(rows) != 1 || rows[0].DetailKey != 2 {
		t.Fatalf("holiday: %v %+v", err, rows)
	}
}

func TestMailSearchWithoutWordsListsByFilter(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0,
		mailMsg(1, fInbox, "<1@x>", "One", 5),
		mailMsg(2, fSent, "<2@x>", "Two", 4)))
	rows, _, err := s.MailSearch(ctx, "  ", MailFilter{Folder: "sent"})
	if err != nil || len(rows) != 1 || rows[0].DetailKey != 2 {
		t.Fatalf("folder only: %v %+v", err, rows)
	}
	for name, f := range map[string]MailFilter{
		"from":  {From: "ann"},
		"since": {Since: mailT0.AddDate(0, 0, -30)},
		"until": {Until: mailT0},
	} {
		if rows, _, err := s.MailSearch(ctx, "", f); err != nil || len(rows) != 2 {
			t.Fatalf("%s only: %v %d", name, err, len(rows))
		}
	}
}

func TestMailSearchUsageErrors(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "One", 5)))
	for _, q := range []string{"", `"`, "*"} {
		_, _, err := s.MailSearch(ctx, q, MailFilter{})
		var c *errs.Coded
		if !errors.As(err, &c) || c.Code != errs.CodeUsage || !strings.Contains(c.Fix, "m365crawl messages") {
			t.Fatalf("query %q: %v", q, err)
		}
	}
	// An unbalanced quote is read as a phrase, the same as for Teams.
	if rows, _, err := s.MailSearch(ctx, `"one`, MailFilter{}); err != nil || len(rows) != 1 {
		t.Fatalf("unbalanced: %v %d", err, len(rows))
	}
	if _, _, err := s.MailSearch(ctx, "one", MailFilter{Folder: "nowhere"}); !errors.Is(err, ErrUnknownMailFolder) {
		t.Fatalf("unknown folder: %v", err)
	}
}

func TestHasMail(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if has, err := s.HasMail(ctx); err != nil || has {
		t.Fatalf("empty archive: %v %v", has, err)
	}
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "One", 5)))
	if has, err := s.HasMail(ctx); err != nil || !has {
		t.Fatalf("with mail: %v %v", has, err)
	}
}

func TestMailSearchAndHasMailFail(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "Alpha", 5)))
	}
	t.Run("search", func(t *testing.T) {
		sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
			_, _, err := s.MailSearch(ctx, "alpha", MailFilter{})
			return err
		})
	})
	t.Run("has mail", func(t *testing.T) {
		sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
			_, err := s.HasMail(ctx)
			return err
		})
	})
}

func TestHasMailOnAnArchiveWithoutMailTables(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.db.ExecContext(ctx, `drop table mail_messages`); err != nil {
		t.Fatal(err)
	}
	if has, err := s.HasMail(ctx); err != nil || has {
		t.Fatalf("no mail tables: %v %v", has, err)
	}
}

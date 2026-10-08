package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

// tSearchSeed is tSeed with the text of call-1's two parts and call-5's one part fetched.
func tSearchSeed(t *testing.T) (*Store, time.Time) {
	t.Helper()
	ctx := context.Background()
	s := newStore(t)
	tSeed(t, s)
	tDerive(t, s, tAcctA)
	tSharePointHosts(t, s)
	at := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM1", okResult(at,
		transcripts.Entry{Speaker: "Ada Example", StartMS: ms(0), Text: "the budget review starts"},
		transcripts.Entry{Speaker: "Bo Example", StartMS: ms(60_000), Text: "no budget yet"},
		transcripts.Entry{Speaker: "Ada Example", StartMS: ms(120_000), Text: "another topic"})))
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM2", okResult(at, transcripts.Entry{Speaker: "Bo Example", StartMS: ms(30_000), Text: "the budget is fine"})))
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM5", okResult(at, transcripts.Entry{Speaker: "Cy Example", Text: "budget again"})))
	return s, at
}

// tSharePointHosts gives the derived parts a host under a SharePoint domain, so what counts as
// fetchable does not hang on how the fetch judges the seed's test host.
func tSharePointHosts(t *testing.T, s *Store) {
	t.Helper()
	s.qExec(t, `update transcript_parts set host='contoso.sharepoint.com' where host<>''`)
}

func hitList(hits []TranscriptHit) string {
	var out []string
	for _, h := range hits {
		out = append(out, fmt.Sprintf("%s#%d %s %dx %s: %s", h.CallID, h.Ordinal, h.At.Format("01-02T15:04:05"), h.Matches, h.Speaker, h.Text))
	}
	return strings.Join(out, "\n")
}

func TestTranscriptSearchCollapsesPerPart(t *testing.T) {
	ctx := context.Background()
	s, at := tSearchSeed(t)
	hits, truncated, err := s.TranscriptSearch(ctx, "budget", TranscriptFilter{})
	if err != nil || truncated {
		t.Fatal(truncated, err)
	}
	want := "call-5#1 11-07T10:00:00 1x Cy Example: budget again\n" +
		"call-1#2 11-03T10:02:30 1x Bo Example: the budget is fine\n" +
		"call-1#1 11-03T10:00:00 2x Ada Example: the budget review starts"
	if got := hitList(hits); got != want {
		t.Fatalf("hits:\n%s\nwant:\n%s", got, want)
	}
	h := hits[2]
	if h.AccountID != tAcctA || h.ThreadID != tThread || h.PartKey != "d:b!d1/ITEM1" || h.Title != "Weekly sync" || h.EventKey != "" || h.FetchedAt == nil || !h.FetchedAt.Equal(at) {
		t.Fatalf("hit %+v", h)
	}
	// The words look in the text only: a speaker's name is --from's.
	if hits, _, err := s.TranscriptSearch(ctx, "Ada", TranscriptFilter{}); err != nil || len(hits) != 0 {
		t.Fatalf("speaker as words: %s, %v", hitList(hits), err)
	}
	if hits, _, err := s.TranscriptSearch(ctx, "budg*", TranscriptFilter{Limit: 1}); err != nil || len(hits) != 1 || hits[0].CallID != "call-5" {
		t.Fatalf("prefix and limit: %s, %v", hitList(hits), err)
	}
	if _, truncated, _ := s.TranscriptSearch(ctx, "budget", TranscriptFilter{Limit: 2}); !truncated {
		t.Fatal("a cut list is truncated")
	}
	if _, truncated, _ := s.TranscriptSearch(ctx, "budget", TranscriptFilter{Limit: 3}); truncated {
		t.Fatal("an exact fit is not truncated")
	}
}

func TestTranscriptSearchFilters(t *testing.T) {
	ctx := context.Background()
	s, _ := tSearchSeed(t)
	for name, c := range map[string]struct {
		query string
		f     TranscriptFilter
		want  string
	}{
		"speaker":          {"budget", TranscriptFilter{Speaker: "BO EX"}, "call-1#2 11-03T10:02:30 1x Bo Example: the budget is fine\ncall-1#1 11-03T10:01:00 1x Bo Example: no budget yet"},
		"speaker no words": {"", TranscriptFilter{Speaker: "cy"}, "call-5#1 11-07T10:00:00 1x Cy Example: budget again"},
		"since":            {"budget", TranscriptFilter{Since: time.Date(2026, 11, 3, 10, 0, 30, 0, time.UTC), Until: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)}, "call-1#2 11-03T10:02:30 1x Bo Example: the budget is fine\ncall-1#1 11-03T10:01:00 1x Bo Example: no budget yet"},
		"until":            {"", TranscriptFilter{Until: time.Date(2026, 11, 3, 10, 2, 0, 0, time.UTC)}, "call-1#1 11-03T10:00:00 2x Ada Example: the budget review starts"},
		"until in a part":  {"", TranscriptFilter{Until: time.Date(2026, 11, 3, 10, 1, 0, 0, time.UTC)}, "call-1#1 11-03T10:00:00 1x Ada Example: the budget review starts"},
		"account":          {"budget", TranscriptFilter{Account: &acctB}, ""},
		"own account":      {"again", TranscriptFilter{Account: &acctA}, "call-5#1 11-07T10:00:00 1x Cy Example: budget again"},
		"phrase":           {`"budget is"`, TranscriptFilter{}, "call-1#2 11-03T10:02:30 1x Bo Example: the budget is fine"},
	} {
		hits, _, err := s.TranscriptSearch(ctx, c.query, c.f)
		if err != nil || hitList(hits) != c.want {
			t.Errorf("%s:\n%s\nwant:\n%s\n%v", name, hitList(hits), c.want, err)
		}
	}
	for _, q := range []string{"", `""`} {
		_, _, err := s.TranscriptSearch(ctx, q, TranscriptFilter{})
		if coded := (*errs.Coded)(nil); !errors.As(err, &coded) || coded.Code != errs.CodeUsage || strings.Contains(coded.Fix, "messages") || !strings.Contains(coded.Fix, "m365crawl transcripts") {
			t.Errorf("%q: %v", q, err)
		}
	}
}

func TestTranscriptSearchEmptyArchive(t *testing.T) {
	s := newStore(t)
	hits, truncated, err := s.TranscriptSearch(context.Background(), "budget", TranscriptFilter{})
	if err != nil || truncated || len(hits) != 0 {
		t.Fatal(hits, truncated, err)
	}
	if has, err := s.HasTranscriptText(context.Background()); err != nil || has {
		t.Fatal(has, err)
	}
}

func TestTranscriptStatus(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	st, err := s.TranscriptStatus(ctx)
	if err != nil || st != (TranscriptStatus{}) {
		t.Fatalf("empty: %+v, %v", st, err)
	}
	s, at := tSearchSeed(t)
	if has, err := s.HasTranscriptText(ctx); err != nil || !has {
		t.Fatal(has, err)
	}
	later := at.Add(time.Hour)
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM5", transcripts.FetchResult{State: transcripts.StateNoAccess, HTTPStatus: 403, At: later}))
	st, err = s.TranscriptStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// call-4 has only a transcript notice: a call, and no part.
	if st.Calls != 5 || st.Parts != 5 || st.Fetchable != 3 || st.Fetched != 3 || st.LastFetchAt == nil || !st.LastFetchAt.Equal(later) {
		t.Fatalf("status %+v", st)
	}
}

// A part fetched with no entries is fetched: status and the text question agree.
func TestTranscriptFetchedWithNoEntries(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tSeed(t, s)
	tDerive(t, s, tAcctA)
	tSharePointHosts(t, s)
	at := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	// A failed attempt is not a fetch.
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM5", transcripts.FetchResult{State: transcripts.StateFailed, At: at}))
	if has, err := s.HasTranscriptText(ctx); err != nil || has {
		t.Fatalf("failed attempt: %v, %v", has, err)
	}
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM1", okResult(at)))
	has, err := s.HasTranscriptText(ctx)
	st, _ := s.TranscriptStatus(ctx)
	if err != nil || !has || st.Fetched != 1 {
		t.Fatalf("empty fetch: %v, %v, %+v", has, err, st)
	}
}

func TestTranscriptSearchUsageFitsTranscripts(t *testing.T) {
	s := newStore(t)
	_, _, err := s.TranscriptSearch(context.Background(), " ", TranscriptFilter{})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeUsage || strings.Contains(coded.Fix, "messages") || !strings.Contains(coded.Fix, "--from") {
		t.Fatalf("%v", err)
	}
}

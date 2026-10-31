package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func setSeam[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// engineRun is a coverage run over an empty archive at covNow, with the Outlook source on.
func engineRun(t *testing.T) (*coverageRun, *runtime) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "m365crawl.db") // a directory of the archive's own, as Windows requires
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: path, now: func() time.Time { return covNow }, outlookOn: true}
	run, err := rt.newCoverageRun(st)
	if err != nil {
		t.Fatal(err)
	}
	return run, rt
}

func day(n int) time.Time { return time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -n) }

func codes(e coverageEntry) string {
	var out []string
	for _, r := range e.Reasons {
		out = append(out, r.Code)
	}
	return strings.Join(out, ",")
}

func TestCoverageSpanHelpers(t *testing.T) {
	setSeam(t, &displayZone, time.UTC)
	a, b := day(2), day(1)
	spans := []store.TimeSpan{{From: day(3), To: a.Add(time.Hour)}, {From: a.Add(2 * time.Hour), To: a.Add(3 * time.Hour)}, {From: b, To: day(0)}}
	pieces := clip(spans, a, b)
	if len(pieces) != 2 || !pieces[0].From.Equal(a) || !pieces[1].To.Equal(a.Add(3*time.Hour)) {
		t.Fatalf("clip %v", pieces)
	}
	h := holes(pieces, a, b)
	if len(h) != 2 || !h[0].From.Equal(a.Add(time.Hour)) || !h[1].To.Equal(b) {
		t.Fatalf("holes %v", h)
	}
	if len(holes(nil, a, b)) != 1 || len(holes([]store.TimeSpan{{From: a, To: b}}, a, b)) != 0 {
		t.Fatal("holes of none and of all")
	}
	e := newCoverageEntry("mail", "sent", "outlook/x")
	e.settle(pieces, a, b)
	e.reason(reasonBeforeCache, "x")
	e.reason(reasonBeforeCache, "again")
	e.finish()
	if e.State != covPartial || len(e.Reasons) != 1 || e.Fix != coverageFixes[reasonBeforeCache] ||
		coverageLine(e) != "mail sent outlook/x: partial (0 items; covered 2026-09-08 00:00 UTC to 2026-09-08 03:00 UTC) before_cache" {
		t.Fatalf("entry %+v %q", e, coverageLine(e))
	}
	e = newCoverageEntry("calendar", "", "")
	e.settle(nil, a, b)
	e.downgrade()
	if e.State != covNotCovered || e.Account != nil || coverageLine(e) != "calendar: not_covered (0 items)" {
		t.Fatalf("entry %+v", e)
	}
}

func mailEvidence(spans []store.TimeSpan, fresh, server time.Time, roles ...string) *mailCoverageEvidence {
	ev := &mailCoverageEvidence{spans: store.MailSpans{Recorded: true, Spans: map[string][]store.TimeSpan{store.MailRoleSent: spans, store.MailRoleInbox: spans}, FreshAt: fresh}, server: server, roles: map[string]bool{}}
	for _, r := range roles {
		ev.roles[r] = true
	}
	return ev
}

func roleCount(n int, state string) func(*store.Store, context.Context, string, string, time.Time, time.Time) (store.MailCopyCheck, error) {
	return func(*store.Store, context.Context, string, string, time.Time, time.Time) (store.MailCopyCheck, error) {
		return store.MailCopyCheck{Messages: n, State: state, LackingInCurrentRange: 1}, nil
	}
}

// Each mail verdict and its reason, judged from the evidence alone.
func TestMailEntryVerdicts(t *testing.T) {
	const acct = "outlook/x"
	setSeam(t, &workIQMailComplete, func(*store.Store, context.Context, string) (bool, error) { return false, nil })
	setSeam(t, &workIQMailBindable, func(*store.Store, context.Context, string) (bool, error) { return true, nil })
	whole := []store.TimeSpan{{From: day(20), To: day(0)}}
	for _, c := range []struct {
		name       string
		ev         *mailCoverageEvidence
		state      string
		a, b       time.Time
		want, base string
		health     []store.SourceHealth
		off        bool
		next       bool
	}{
		{name: "no folder", ev: mailEvidence(whole, day(0), time.Time{}), state: "consistent", a: day(5), b: day(4), want: "folder_not_synced"},
		{name: "future", ev: mailEvidence(whole, day(0), time.Time{}, "sent"), state: "consistent", a: day(-2), b: day(-3), want: "future"},
		{name: "covered", ev: mailEvidence(whole, day(0), time.Time{}, "sent"), state: "consistent", a: day(5), b: day(4), base: basisCache},
		{name: "no complete read", ev: mailEvidence(nil, day(0), time.Time{}, "sent"), state: "consistent", a: day(5), b: day(4), want: "no_complete_read"},
		{name: "server", ev: mailEvidence(nil, day(0), day(1), "sent"), state: "partial", a: day(5), b: day(4), base: basisServer},
		{name: "both", ev: mailEvidence(whole, day(0), day(1), "sent"), state: "consistent", a: day(5), b: day(4), base: basisBoth},
		{name: "joined", ev: mailEvidence([]store.TimeSpan{{From: day(5).Add(time.Hour), To: day(0)}}, day(0), day(5).Add(2*time.Hour), "sent"), state: "consistent", a: day(5), b: day(4), base: basisBoth},
		{name: "older copy", ev: mailEvidence(whole, day(0), time.Time{}, "sent"), state: "older_copy_only", a: day(30), b: day(29), want: "only_older_cache_copy", next: true},
		{name: "before cache", ev: mailEvidence(whole, day(0), time.Time{}, "sent"), state: "consistent", a: day(30), b: day(29), want: "before_cache", next: true},
		{name: "copies disagree", ev: mailEvidence(whole, day(0), time.Time{}, "sent"), state: "partial", a: day(5), b: day(4), want: "folder_copies_disagree", base: basisCache, next: true},
		{name: "source off", ev: mailEvidence([]store.TimeSpan{{From: day(20), To: day(5)}}, day(0), time.Time{}, "sent"), state: "consistent", a: day(5), b: day(4), want: "source_off", off: true},
		{name: "failing", ev: mailEvidence([]store.TimeSpan{{From: day(20), To: day(5)}}, day(0), time.Time{}, "sent"), state: "consistent", a: day(5), b: day(4), want: "source_failing",
			health: []store.SourceHealth{{Source: "outlook_mail", AccountID: new(string), State: "failing"}, {Source: "outlook_mail", State: "failing"}}},
		{name: "stale", ev: mailEvidence([]store.TimeSpan{{From: day(20), To: day(5)}}, day(0), time.Time{}, "sent"), state: "consistent", a: day(5), b: day(4), want: "after_cache_fresh",
			health: []store.SourceHealth{{Source: "outlook_mail", State: store.SourceStale}, {Source: "teams", State: "failing"}}},
		{name: "newest mail", ev: mailEvidence([]store.TimeSpan{{From: day(20), To: day(5)}}, day(0), time.Time{}, "sent"), state: "consistent", a: day(5), b: day(4), want: "after_newest_mail"},
		{name: "last read", ev: mailEvidence([]store.TimeSpan{{From: day(20), To: day(5)}}, day(5), time.Time{}, "sent"), state: "consistent", a: day(5), b: day(4), want: "after_last_read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			run, rt := engineRun(t)
			rt.outlookOn = !c.off
			run.health = c.health
			run.mail[acct] = c.ev
			setSeam(t, &mailRoleCount, roleCount(3, c.state))
			e, err := run.mailEntry(acct, "sent", nil, c.a, c.b)
			if err != nil {
				t.Fatal(err)
			}
			if codes(e) != c.want || e.Basis != c.base || (len(e.Next) > 0) != c.next || e.Items != 3 {
				t.Fatalf("got %s %q basis %q next %v", e.State, codes(e), e.Basis, e.Next)
			}
		})
	}
}

// The Sent span starts later by the longest draft-to-send delay, and the reason says so.
func TestMailEntrySentShift(t *testing.T) {
	run, _ := engineRun(t)
	ev := mailEvidence([]store.TimeSpan{{From: day(20), To: day(0)}}, day(0), time.Time{}, "sent", "inbox")
	ev.spans.SentShift = 48 * time.Hour
	run.mail["outlook/x"] = ev
	setSeam(t, &mailRoleCount, roleCount(0, "consistent"))
	setSeam(t, &workIQMailBindable, func(*store.Store, context.Context, string) (bool, error) { return false, nil })
	e, err := run.mailEntry("outlook/x", "sent", nil, day(19), day(18))
	if err != nil || codes(e) != "before_cache" || !strings.Contains(e.Reasons[0].Detail, "drafted up to 48h0m0s") || e.Next != nil {
		t.Fatalf("%+v %v", e, err)
	}
	if e, _ := run.mailEntry("outlook/x", "inbox", nil, day(19), day(18)); e.State != covCovered {
		t.Fatalf("inbox %+v", e)
	}
	if e, _ := run.mailEntry("outlook/x", "sent", nil, day(25), day(24)); strings.Contains(e.Reasons[0].Detail, "drafted") != true {
		t.Fatalf("before %+v", e)
	}
}

func TestMailEntryReportsEvidenceFailures(t *testing.T) {
	fail := errors.New("broken")
	t.Run("spans", func(t *testing.T) {
		run, _ := engineRun(t)
		setSeam(t, &mailCoverageSpans, func(*store.Store, context.Context, string) (store.MailSpans, error) { return store.MailSpans{}, fail })
		if _, err := run.mailEntry("outlook/x", "sent", nil, day(5), day(4)); !errors.Is(err, fail) {
			t.Fatal(err)
		}
	})
	t.Run("server", func(t *testing.T) {
		run, _ := engineRun(t)
		setSeam(t, &mailServerThrough, func(*store.Store, context.Context, string) (time.Time, error) { return time.Time{}, fail })
		if _, err := run.mailEntry("outlook/x", "sent", nil, day(5), day(4)); !errors.Is(err, fail) {
			t.Fatal(err)
		}
	})
	t.Run("count", func(t *testing.T) {
		run, _ := engineRun(t)
		setSeam(t, &mailRoleCount, func(*store.Store, context.Context, string, string, time.Time, time.Time) (store.MailCopyCheck, error) {
			return store.MailCopyCheck{}, fail
		})
		if _, err := run.mailEntry("outlook/x", "sent", nil, day(5), day(4)); !errors.Is(err, fail) {
			t.Fatal(err)
		}
	})
	t.Run("route", func(t *testing.T) {
		run, _ := engineRun(t)
		run.mail["outlook/x"] = mailEvidence(nil, day(0), time.Time{}, "sent")
		setSeam(t, &mailRoleCount, roleCount(0, "older_copy_only"))
		setSeam(t, &workIQMailComplete, func(*store.Store, context.Context, string) (bool, error) { return false, fail })
		run.mail["outlook/x"].spans.Spans[store.MailRoleSent] = []store.TimeSpan{{From: day(3), To: day(2)}}
		if _, err := run.mailEntry("outlook/x", "sent", nil, day(5), day(4)); !errors.Is(err, fail) {
			t.Fatal(err)
		}
	})
	t.Run("legacy notice and memo", func(t *testing.T) {
		run, _ := engineRun(t)
		calls := 0
		setSeam(t, &mailCoverageSpans, func(*store.Store, context.Context, string) (store.MailSpans, error) {
			calls++
			return store.MailSpans{Legacy: true}, nil
		})
		for range 2 {
			if _, err := run.mailEntry("outlook/x", "sent", []store.MailFolderRow{{Account: "outlook/x", Kind: "to_me"}, {Account: "outlook/y", Kind: "sent"}}, day(5), day(4)); err != nil {
				t.Fatal(err)
			}
		}
		if calls != 1 || len(run.notices()) != 1 || !run.mail["outlook/x"].roles["inbox"] || run.mail["outlook/x"].roles["sent"] {
			t.Fatalf("calls %d notices %v roles %v", calls, run.notices(), run.mail["outlook/x"].roles)
		}
	})
}

// A route is looked up once per account and range; one that does not apply gives no steps.
func TestMailStepsAreLookedUpOnce(t *testing.T) {
	run, _ := engineRun(t)
	asked := 0
	setSeam(t, &workIQMailComplete, func(*store.Store, context.Context, string) (bool, error) { asked++; return true, nil })
	for range 2 {
		if steps, _, err := run.mailSteps("outlook/x", day(5), day(4)); err != nil || steps != nil {
			t.Fatalf("%v %v", steps, err)
		}
	}
	if _, _, err := run.mailSteps("outlook/x", day(4), day(3)); err != nil {
		t.Fatal(err)
	}
	if asked != 2 || run.addRoutes(nil, []coverageEntry{newCoverageEntry("mail", "sent", "")}) != nil {
		t.Fatalf("asked %d", asked)
	}
}

func TestMailNoAccountSaysWhy(t *testing.T) {
	run, rt := engineRun(t)
	setSeam(t, &mailProfileCount, func(*runtime) (int, *mailSourceError) { return 1, nil })
	if e := run.mailNoAccount("sent"); codes(e) != "never_read" {
		t.Fatalf("%+v", e)
	}
	setSeam(t, &mailProfileCount, func(*runtime) (int, *mailSourceError) { return 0, nil })
	if e := run.mailNoAccount("sent"); codes(e) != "no_profile" {
		t.Fatalf("%+v", e)
	}
	rt.outlookOn = false
	if e := run.mailNoAccount("sent"); codes(e) != "source_off" || e.Account != nil {
		t.Fatalf("%+v", e)
	}
}

func chatEv(ev store.ChatEvidence) func(*store.Store, context.Context, string, string, string, time.Time, time.Time, store.ChatNarrow) (store.ChatEvidence, error) {
	ev.Matched = true
	return func(*store.Store, context.Context, string, string, string, time.Time, time.Time, store.ChatNarrow) (store.ChatEvidence, error) {
		return ev, nil
	}
}

// Each chat verdict and its reason, judged from the evidence alone.
func TestChatEntryVerdicts(t *testing.T) {
	for _, c := range []struct {
		name   string
		reads  []time.Time
		ev     store.ChatEvidence
		a, b   time.Time
		state  string
		want   string
		health []store.SourceHealth
		stale  time.Time
		next   bool
	}{
		{name: "never read", ev: store.ChatEvidence{Items: 2}, a: day(5), b: day(4), state: covNotCovered, want: "never_read"},
		{name: "future", reads: []time.Time{day(20)}, a: day(-2), b: day(-3), state: covNotCovered, want: "future"},
		{name: "covered", reads: []time.Time{day(20), day(3)}, ev: store.ChatEvidence{Items: 2, MyItems: 1}, a: day(5), b: day(4), state: covCovered},
		{name: "before start, nothing held", reads: []time.Time{day(20), day(3)}, a: day(30), b: day(29), state: covNotCovered, want: "before_archive_start", next: true},
		{name: "before start, some held", reads: []time.Time{day(20), day(3)}, ev: store.ChatEvidence{Deleted: 1}, a: day(30), b: day(29), state: covPartial, want: "before_archive_start", next: true},
		{name: "failing", reads: []time.Time{day(20), day(6)}, a: day(5), b: day(4), state: covNotCovered, want: "source_failing",
			health: []store.SourceHealth{{Source: "teams", State: "failing"}}},
		{name: "not writing", reads: []time.Time{day(20), day(6)}, stale: day(5), a: day(5), b: day(4), state: covNotCovered, want: "after_cache_fresh"},
		{name: "last read", reads: []time.Time{day(20), day(6)}, stale: day(6).Add(time.Minute), a: day(5), b: day(4), state: covNotCovered, want: "after_last_read"},
		{name: "read gap", reads: []time.Time{day(20), day(19), day(2)}, a: day(15), b: day(14), state: covPartial, want: "read_gap"},
		{name: "read gap inside a long range", reads: []time.Time{day(20), day(19), day(2)}, a: day(19).Add(time.Hour), b: day(3), state: covPartial, want: "read_gap"},
		{name: "daily reads", reads: []time.Time{day(20), day(19), day(18), day(17), day(16), day(15), day(14), day(13), day(12), day(6)}, a: day(19), b: day(13), state: covCovered},
		{name: "behind and gaps", reads: []time.Time{day(20), day(3)}, ev: store.ChatEvidence{Behind: 1, GapConversations: 1, PossiblyMissing: 4}, a: day(5), b: day(4), state: covPartial, want: "conversation_behind,sequence_gap"},
	} {
		t.Run(c.name, func(t *testing.T) {
			run, _ := engineRun(t)
			run.health = c.health
			setSeam(t, &teamsReads, func(*store.Store, context.Context, string) (store.TeamsReadLog, error) {
				return store.TeamsReadLog{Reads: c.reads, LastUnchanged: c.stale}, nil
			})
			setSeam(t, &chatEvidence, chatEv(c.ev))
			e, _, err := run.chatEntry("t", "u", store.ChatScopeChats, c.a, c.b, store.ChatNarrow{})
			if err != nil {
				t.Fatal(err)
			}
			if e.State != c.state || codes(e) != c.want || (len(e.Next) > 0) != c.next || e.Items != c.ev.Items || *e.MyItems != c.ev.MyItems || *e.DeletedItems != c.ev.Deleted {
				t.Fatalf("got %s %q next %v %+v", e.State, codes(e), e.Next, e)
			}
		})
	}
}

func TestChatEntryReportsEvidenceFailures(t *testing.T) {
	fail := errors.New("broken")
	run, _ := engineRun(t)
	setSeam(t, &chatEvidence, func(*store.Store, context.Context, string, string, string, time.Time, time.Time, store.ChatNarrow) (store.ChatEvidence, error) {
		return store.ChatEvidence{}, fail
	})
	if _, _, err := run.chatEntry("t", "u", store.ChatScopeChats, day(5), day(4), store.ChatNarrow{}); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	setSeam(t, &chatEvidence, chatEv(store.ChatEvidence{}))
	setSeam(t, &teamsReads, func(*store.Store, context.Context, string) (store.TeamsReadLog, error) {
		return store.TeamsReadLog{}, fail
	})
	if _, _, err := run.chatEntry("t", "u", store.ChatScopeChats, day(5), day(4), store.ChatNarrow{}); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	// The log is read once per account; the WorkIQ steps once too.
	run, _ = engineRun(t)
	calls := 0
	setSeam(t, &teamsReads, func(*store.Store, context.Context, string) (store.TeamsReadLog, error) {
		calls++
		return store.TeamsReadLog{Reads: []time.Time{day(3)}}, nil
	})
	for range 2 {
		if e, _, err := run.chatEntry("t", "u", store.ChatScopeChats, day(5), day(4), store.ChatNarrow{}); err != nil || len(e.Next) == 0 {
			t.Fatal(e, err)
		}
	}
	if calls != 1 || len(run.addRoutes(nil, []coverageEntry{{Source: "chats", Account: new("t/u"), Next: []startStep{{}}}})) != 1 {
		t.Fatalf("calls %d", calls)
	}
	// A WorkIQ setup that cannot be read is an error.
	run, _ = engineRun(t)
	_ = run.st.Close()
	if _, _, err := run.chatEntry("t", "u", store.ChatScopeChats, day(5), day(4), store.ChatNarrow{}); err == nil {
		t.Fatal("a closed archive gave WorkIQ steps")
	}
}

// Copies an older folder set or WorkIQ holds are dated before the last read or the server spans'
// end, so the folder-copy check of a range ending later (today, an open-ended read) is judged up
// to there: a window proven that far ends the disagreement and its route instead of asking for a
// new window on every read (#313).
func TestCopiesDisagreeAreJudgedUpToTheLastReadOrServerEnd(t *testing.T) {
	setSeam(t, &workIQMailComplete, func(*store.Store, context.Context, string) (bool, error) { return false, nil })
	setSeam(t, &workIQMailBindable, func(*store.Store, context.Context, string) (bool, error) { return true, nil })
	setSeam(t, &mailRoleCount, roleCount(3, "partial"))
	whole := []store.TimeSpan{{From: day(20), To: day(1)}}
	run, _ := engineRun(t)
	ev := mailEvidence(whole, day(1), time.Time{}, "inbox")
	ev.windows = []store.TimeSpan{{From: day(5), To: day(1).Add(time.Hour)}}
	run.mail["outlook/x"] = ev
	e, err := run.mailEntry("outlook/x", "inbox", nil, day(5), covNow.Add(24*time.Hour))
	if err != nil || e.has(reasonCopiesDisagree) || len(e.Next) != 0 {
		t.Fatalf("a range past now asked again for proven mail: %s %v %v", codes(e), e.Next, err)
	}
	// Without a proven window the range up to the last read is routed, once.
	run, _ = engineRun(t)
	run.mail["outlook/x"] = mailEvidence(whole, day(1), time.Time{}, "inbox")
	e, err = run.mailEntry("outlook/x", "inbox", nil, day(5), covNow.Add(24*time.Hour))
	if err != nil || !e.has(reasonCopiesDisagree) || len(e.Next) == 0 ||
		!strings.Contains(e.Next[len(e.Next)-2].Command, "--until "+day(1).Add(time.Hour).Format(time.RFC3339)) {
		t.Fatalf("route %s %v %v", codes(e), e.Next, err)
	}
	// A last read before the range says nothing about it: the whole range is judged.
	run, _ = engineRun(t)
	run.mail["outlook/x"] = mailEvidence(whole, day(10), time.Time{}, "inbox")
	if e, err = run.mailEntry("outlook/x", "inbox", nil, day(5), day(4)); err != nil || !e.has(reasonCopiesDisagree) {
		t.Fatalf("early read %s %v", codes(e), err)
	}
}

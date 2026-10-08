package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
)

// appDescription is the one sentence that says what m365crawl is: the root help's description and
// the crawlkit manifest's.
const appDescription = "Mirror Microsoft 365 on this machine — Teams chats, Outlook mail and your calendar — into one local SQLite archive that agents can read offline."

// startStep is one line of the "Start here" block: a command and what it is for.
type startStep struct {
	Command string `json:"command"`
	Does    string `json:"does"`
	mail    bool   // a mail command, which this operating system may not run
}

// startHere is the "Start here" block of the root help and the overview, in order.
var startHere = []startStep{
	{Command: "m365crawl sync", Does: "read Teams, Outlook mail and the calendar into the archive"},
	{Command: "m365crawl", Does: "what the archive holds and how fresh it is"},
	{Command: `m365crawl search "words"`, Does: "find anything across chats and mail"},
	{Command: "m365crawl calendar", Does: "today's meetings; calendar event <id> for one meeting with its chat and mail"},
	{Command: "m365crawl mail unread", Does: "unread mail by folder", mail: true},
	{Command: "m365crawl unread", Does: "unread Teams chats"},
	{Command: "m365crawl mail list --has-attachments", Does: "mail with files; mail show <id> lists each file's name, size and type", mail: true},
}

// startHereColumn is where the descriptions of the block start, after a two-space indent.
const startHereColumn = 28

// startHereText prints steps as the "Start here" block: commands in one column, descriptions after.
func startHereText(steps []startStep) string {
	var b strings.Builder
	b.WriteString("Start here:\n")
	for _, s := range steps {
		pad := max(startHereColumn-len(s.Command), 3)
		b.WriteString("  " + s.Command + strings.Repeat(" ", pad) + s.Does + "\n")
	}
	return b.String()
}

// stepsHere are the Start here steps that apply on this operating system.
func stepsHere() []startStep {
	out := []startStep{}
	for _, s := range startHere {
		if !s.mail || mailSupported() {
			out = append(out, s)
		}
	}
	return out
}

// Source states of the overview.
const (
	overviewOK          = "ok"
	overviewEmpty       = "empty"
	overviewNoArchive   = "no_archive"
	overviewUnsupported = "unsupported"
)

// overviewSource is one source in the overview. Counts and times that do not apply to the source
// are omitted; a time the archive does not have yet is null.
type overviewSource struct {
	Source        string     `json:"source"`
	State         string     `json:"state"`
	Conversations *int       `json:"conversations,omitempty"`
	Messages      *int       `json:"messages,omitempty"`
	Events        *int       `json:"events,omitempty"`
	NewestAt      *time.Time `json:"newest_at,omitempty"`
	OldestAt      *time.Time `json:"oldest_at,omitempty"`
	WindowStart   *time.Time `json:"window_start,omitempty"`
	WindowEnd     *time.Time `json:"window_end,omitempty"`
	CoverageGap   *bool      `json:"coverage_gap,omitempty"`
	LastSyncAt    *time.Time `json:"last_sync_at"`
	Note          string     `json:"note,omitempty"`
}

// overviewResult is what a bare `m365crawl` prints.
type overviewResult struct {
	ArchivePath   string           `json:"archive_path"`
	ArchiveExists bool             `json:"archive_exists"`
	Sources       []overviewSource `json:"sources"`
	Next          []startStep      `json:"next"`
	Note          string           `json:"note,omitempty"`
}

// overviewCmd is what m365crawl does with no command: what the archive holds per source and how
// fresh it is, then where to start. It only reads: it never runs the implicit sync, and it works
// before the first sync.
type overviewCmd struct{}

const noArchiveYet = "no archive yet; run m365crawl sync"

func (overviewCmd) Run(rt *runtime) error {
	res := &overviewResult{ArchivePath: rt.dbPath, Next: stepsHere()}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	switch {
	case errors.Is(err, store.ErrNoArchive):
		res.Note = noArchiveYet
		for _, s := range []string{"chats", "mail", "calendar"} {
			src := overviewSource{Source: s, State: overviewNoArchive}
			if s == "mail" && !mailSupported() {
				src.State, src.Note = overviewUnsupported, errs.MailUnsupportedPlatform().Message
			}
			res.Sources = append(res.Sources, src)
		}
		return rt.write("overview", res)
	case err != nil:
		return errs.DBError(err)
	}
	defer func() { _ = st.Close() }()
	res.ArchiveExists = true
	for _, fill := range []func(*store.Store) (overviewSource, error){rt.overviewChats, rt.overviewMail, rt.overviewCalendar} {
		src, err := fill(st)
		if err != nil {
			return asCoded(err)
		}
		res.Sources = append(res.Sources, src)
	}
	return rt.write("overview", res)
}

// at is t as a JSON time, or nil when it is zero.
func at(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func (rt *runtime) overviewChats(st *store.Store) (overviewSource, error) {
	row, err := st.Status(rt.ctx)
	if err != nil {
		return overviewSource{}, err
	}
	var conv, msgs int
	for _, a := range row.Accounts {
		conv, msgs = conv+a.Conversations, msgs+a.Messages
	}
	src := overviewSource{Source: "chats", State: overviewOK, Conversations: &conv, Messages: &msgs, NewestAt: at(row.NewestSentAt), LastSyncAt: at(row.LastSuccessAt)}
	if msgs == 0 {
		src.State, src.Note = overviewEmpty, "no Teams messages archived yet: run m365crawl sync, and m365crawl doctor if it stays empty"
	}
	return src, nil
}

func (rt *runtime) overviewMail(st *store.Store) (overviewSource, error) {
	if !mailSupported() {
		return overviewSource{Source: "mail", State: overviewUnsupported, Note: errs.MailUnsupportedPlatform().Message}, nil
	}
	ms, err := st.MailStatus(rt.ctx)
	if err != nil {
		return overviewSource{}, err
	}
	src := overviewSource{Source: "mail", State: overviewOK, Messages: &ms.Messages, NewestAt: at(ms.NewestAt), OldestAt: at(ms.OldestAt), LastSyncAt: at(ms.SyncedAt)}
	if ms.Messages == 0 {
		src.State, src.Note = overviewEmpty, "no mail archived yet: run m365crawl sync; m365crawl status says whether mail is being read"
	}
	return src, nil
}

// calendarAgendaOf is the test seam of the agenda the overview reads today's coverage from.
var calendarAgendaOf = (*store.Store).CalendarAgenda

func (rt *runtime) overviewCalendar(st *store.Store) (overviewSource, error) {
	start, end, events, err := rt.calendarWindow(st)
	if err != nil {
		return overviewSource{}, err
	}
	src := overviewSource{Source: "calendar", State: overviewOK, Events: &events, WindowStart: at(start), WindowEnd: at(end)}
	if start.IsZero() {
		src.State, src.Note = overviewEmpty, "no calendar archived yet: run m365crawl sync"
		return src, nil
	}
	y, m, d := rt.now().In(displayZone).Date()
	from := time.Date(y, m, d, 0, 0, 0, 0, displayZone) // today, as calendar reads it
	agenda, err := calendarAgendaOf(st, rt.ctx, store.CalendarFilter{Account: rt.account, From: from, To: from.AddDate(0, 0, 1), Limit: 1})
	if err != nil {
		return overviewSource{}, err
	}
	src.CoverageGap = &agenda.Gap
	if agenda.Gap {
		src.Note = "today is not fully covered by cached calendar data: run m365crawl sync, or open the calendar in Teams or Outlook"
	}
	var synced time.Time
	for _, a := range agenda.Accounts {
		if a.SyncedAt.After(synced) {
			synced = a.SyncedAt
		}
	}
	src.LastSyncAt = at(synced)
	return src, nil
}

// renderOverview prints the overview for a person: one line per source, then the Start here block.
func (rt *runtime) renderOverview(r *overviewResult) {
	w, color := rt.stdout, rt.color
	if !r.ArchiveExists {
		_, _ = fmt.Fprintf(w, "%s\n\n", render.Dim(r.Note, color))
	} else {
		rows := make([][]string, len(r.Sources))
		for i, s := range r.Sources {
			rows[i] = []string{s.Source, s.State, overviewHolds(s), overviewTime(s.LastSyncAt)}
		}
		render.Table(w, []string{"source", "state", "holds", "last sync"}, rows, color)
		for _, s := range r.Sources {
			if s.Note != "" {
				_, _ = fmt.Fprintf(w, "%s\n", render.Dim(s.Source+": "+s.Note, color))
			}
		}
		_, _ = fmt.Fprintln(w)
	}
	_, _ = io.WriteString(w, startHereText(r.Next))
}

func overviewTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return stamp(*t)
}

func overviewDay(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.In(displayZone).Format("2006-01-02")
}

// overviewHolds is a source's counts and span in one phrase.
func overviewHolds(s overviewSource) string {
	switch {
	case s.Conversations != nil:
		return fmt.Sprintf("%s in %s, newest %s", plural(*s.Messages, "message", "messages"), plural(*s.Conversations, "conversation", "conversations"), overviewTime(s.NewestAt))
	case s.Messages != nil:
		return fmt.Sprintf("%s from %s, newest %s", plural(*s.Messages, "message", "messages"), overviewDay(s.OldestAt), overviewTime(s.NewestAt))
	case s.Events != nil:
		gap := ""
		if s.CoverageGap != nil {
			gap = "; today covered"
			if *s.CoverageGap {
				gap = "; today not fully covered"
			}
		}
		return plural(*s.Events, "event", "events") + " from " + overviewDay(s.WindowStart) + " to " + overviewDay(s.WindowEnd) + gap
	}
	return "-"
}

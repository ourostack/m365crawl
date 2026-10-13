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
	{Command: `m365crawl search "words"`, Does: "search chats, mail, fetched transcripts, notes, document metadata and Loop text"},
	{Command: "m365crawl calendar", Does: "today's meetings; calendar event <id> for one meeting with its chat and mail"},
	{Command: "m365crawl mail unread", Does: "unread mail by folder", mail: true},
	{Command: "m365crawl unread", Does: "unread Teams chats"},
	{Command: "m365crawl mail list --has-attachments", Does: "mail with files; mail show <id> lists each file's name, size and type", mail: true},
	{Command: "m365crawl docs list", Does: "Office recent, shared and recommended document metadata and OneDrive usage records; docs sync registers the sources"},
	{Command: `m365crawl notes search "words"`, Does: "OneNote indexed notes; notes sync registers the indexes"},
	{Command: `m365crawl loop search "words"`, Does: "cached Loop channel text (partial snapshots); loop sync registers the archived records"},
	{Command: "m365crawl people", Does: "Teams people, mail correspondents and native Outlook people observations"},
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
	// overviewNotRegistered is a documents or notes source with nothing registered: only its
	// explicit sync command adds one.
	overviewNotRegistered = "not_registered"
)

// overviewKinds are the sources the overview reports, in order.
var overviewKinds = []string{"chats", "mail", "calendar", "documents", "notes", "loop", "people", "transcripts"}

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
	Documents     *int       `json:"documents,omitempty"`
	Notes         *int       `json:"notes,omitempty"`
	People        *int       `json:"people,omitempty"`       // Teams people and mail correspondents
	Observations  *int       `json:"observations,omitempty"` // native Outlook people observations
	Calls         *int       `json:"calls,omitempty"`
	Parts         *int       `json:"parts,omitempty"`
	Fetched       *int       `json:"fetched,omitempty"`
	Registered    *int       `json:"registered_sources,omitempty"`
	Failed        *int       `json:"failed_sources,omitempty"`
	// Loop only: channel observations, sources whose latest snapshot has no channel text, and the
	// archived cache records a loop sync registers from.
	Channels        *int `json:"channels,omitempty"`
	MetadataOnly    *int `json:"metadata_only_sources,omitempty"`
	ArchivedRecords *int `json:"archived_records,omitempty"`
	// LastProjectedAt is Loop's last_sync_at under its plain name: the last Loop projection, not an
	// edit time. Loop only.
	LastProjectedAt *time.Time `json:"last_projected_at,omitempty"`
	// LastSyncAt is the last sync of chats, mail and the calendar, the last admitted capture of
	// documents, notes and native people, the last Loop projection, and the last transcript fetch.
	LastSyncAt *time.Time `json:"last_sync_at"`
	Note       string     `json:"note,omitempty"`
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
		for _, s := range overviewKinds {
			src := overviewSource{Source: s, State: overviewNoArchive}
			if s == "mail" && !mailSupported() {
				src.State, src.Note = overviewUnsupported, "mail is not supported on this operating system"
			} else if s == "mail" && rt.outlookOn {
				if problem := rt.discoverOutlookMail().problem(rt.outlookDefault); problem != nil {
					src.State, src.Note = mailStateUnavailable, mailSourceNote(problem)
				}
			}
			res.Sources = append(res.Sources, src)
		}
		return rt.write("overview", res)
	case err != nil:
		return errs.DBError(err)
	}
	defer func() { _ = st.Close() }()
	res.ArchiveExists = true
	// What chats and the calendar say for an account the archive lacks, or chats for a mail account.
	note, err := rt.unknownAccountNote(st)
	if err != nil {
		return asCoded(err)
	}
	for _, fill := range []func() (overviewSource, error){
		func() (overviewSource, error) { return rt.overviewChats(st, rt.forTeams(note)) },
		func() (overviewSource, error) { return rt.overviewMail(st) },
		func() (overviewSource, error) { return rt.overviewCalendar(st, note) },
	} {
		src, err := fill()
		if err != nil {
			return asCoded(err)
		}
		res.Sources = append(res.Sources, src)
	}
	more, err := rt.overviewArchived(st, res.Sources[0].LastSyncAt)
	if err != nil {
		return asCoded(err)
	}
	res.Sources = append(res.Sources, more...)
	return rt.write("overview", res)
}

// overviewArchived is the documents, notes, people and transcripts rows, from what the archive
// already holds: no source is read or captured.
// chatsSynced is the chats row's last sync, which people inherit when no native capture is newer.
func (rt *runtime) overviewArchived(st *store.Store, chatsSynced *time.Time) ([]overviewSource, error) {
	tenant := ""
	if rt.account != nil {
		tenant = rt.account.TenantID
	}
	counts, err := st.OverviewCounts(rt.ctx, tenant)
	if err != nil {
		return nil, err
	}
	registered := map[string][]store.KnowledgeSource{}
	for _, kind := range []string{"documents", "notes", "outlook_people"} {
		read := func() ([]store.KnowledgeSource, error) { return st.KnowledgeSources(rt.ctx, kind) }
		if kind == "documents" {
			read = func() ([]store.KnowledgeSource, error) { return st.DocumentSources(rt.ctx) }
		}
		if registered[kind], err = read(); err != nil {
			return nil, err
		}
	}
	documents, notes, people := registered["documents"], registered["notes"], registered["outlook_people"]
	transcripts, err := transcriptStatus(rt, st)
	if err != nil {
		return nil, err
	}
	var lastChats time.Time
	if chatsSynced != nil {
		lastChats = *chatsSynced
	}
	unscoped := ""
	if rt.account != nil {
		unscoped = "not narrowed by --account: source ownership is unresolved"
	}
	out := []overviewSource{
		registeredSource("documents", documents, counts.Documents, "no Office document sources registered: run m365crawl docs sync (Mac Office) or docs sync --root DIR",
			"m365crawl docs sources shows each failure and its fix; the last good capture of a failed source stays current", unscoped),
		registeredSource("notes", notes, counts.Notes, "no OneNote indexes registered: run m365crawl notes sync, or notes sync --root DIR",
			"m365crawl notes sources shows each failure; the last good capture of a failed source stays current", unscoped),
	}
	loop, err := st.LoopOverview(rt.ctx)
	if err != nil {
		return nil, err
	}
	out = append(out, loopOverviewSource(loop, unscoped))
	known := counts.TeamsPeople + counts.MailPeople
	ppl := overviewSource{Source: "people", State: overviewOK, People: &known, Observations: &counts.PeopleObservations,
		Registered: intPtr(len(people)), LastSyncAt: at(lastCapture(people, lastChats))}
	if known+counts.PeopleObservations == 0 {
		ppl.State, ppl.Note = overviewEmpty, "no people archived yet: run m365crawl sync; m365crawl people sync adds native Outlook people observations"
	}
	if len(people) > 0 {
		ppl.Note = "native observation identities stay unresolved; m365crawl --max-age 0 people sources shows their coverage (keep --db, omit --account)"
		if rt.account != nil {
			ppl.Note += "; native Outlook observations are " + unscoped
		}
	}
	if rt.account != nil {
		ppl.Note = withNote(ppl.Note, "Teams people are narrowed to the --account tenant; mail correspondents are not narrowed by --account")
	}
	out = append(out, ppl)
	tr := overviewSource{Source: "transcripts", State: overviewEmpty, Calls: intPtr(0), Parts: intPtr(0), Fetched: intPtr(0),
		Note: "no recorded meetings in the archive yet: run m365crawl sync"}
	if transcripts != nil && transcripts.Calls > 0 {
		tr = overviewSource{Source: "transcripts", State: overviewOK, Calls: &transcripts.Calls, Parts: &transcripts.Parts,
			Fetched: &transcripts.Fetched, LastSyncAt: transcripts.LastFetchAt}
		if transcripts.Fetched == 0 {
			tr.Note = "transcript text is fetched explicitly: m365crawl transcripts lists meetings, transcripts fetch <meeting> reads one"
		}
	}
	if rt.account != nil {
		tr.Note = withNote(tr.Note, "not narrowed by --account")
	}
	return append(out, tr), nil
}

// registeredSource is the overview row of a source that only an explicit sync registers.
func registeredSource(name string, sources []store.KnowledgeSource, rows int, none, failedNote, unscoped string) overviewSource {
	src := overviewSource{Source: name, State: overviewOK, Registered: intPtr(len(sources)), LastSyncAt: at(lastCapture(sources, time.Time{}))}
	failed := 0
	for _, s := range sources {
		if s.Status == "failed" || s.Status == "cancelled" {
			failed++
		}
	}
	src.Failed = &failed
	if name == "documents" {
		src.Documents = &rows
	} else {
		src.Notes = &rows
	}
	switch {
	case len(sources) == 0:
		src.State, src.Note = overviewNotRegistered, none
	case failed > 0:
		src.Note = plural(failed, "source", "sources") + " failed the last capture: " + failedNote
	case rows == 0:
		src.State = overviewEmpty
	}
	if unscoped != "" && len(sources) > 0 {
		src.Note = withNote(src.Note, unscoped)
	}
	return src
}

// loopOverviewSource is the overview row of cached Loop channels, from counts alone. It tells
// nothing archived, archived but unregistered, and registered with failed or metadata-only
// sources apart, and names the command for each.
func loopOverviewSource(loop store.LoopOverview, unscoped string) overviewSource {
	src := overviewSource{Source: "loop", State: overviewOK, Registered: &loop.Registered, Failed: &loop.Failed, Channels: &loop.Channels,
		MetadataOnly: &loop.MetadataOnly, ArchivedRecords: &loop.ArchivedRecords, LastSyncAt: loop.LastProjectedAt}
	if src.LastSyncAt != nil {
		src.LastSyncAt = at(*src.LastSyncAt)
		src.LastProjectedAt = src.LastSyncAt
	}
	switch {
	case loop.Registered == 0 && loop.ArchivedRecords == 0:
		src.State, src.Note = overviewNotRegistered, "no cached Loop records are archived yet: run m365crawl sync, then m365crawl loop sync"
	case loop.Registered == 0:
		src.State = overviewNotRegistered
		src.Note = plural(loop.ArchivedRecords, "cached Loop record is", "cached Loop records are") + " archived and none is registered: run m365crawl loop sync"
	default:
		if loop.Channels == 0 {
			src.State = overviewEmpty
		}
		problems := []string{}
		if loop.Failed > 0 {
			problems = append(problems, plural(loop.Failed, "source", "sources")+" failed the last projection")
		}
		if loop.MetadataOnly > 0 {
			problems = append(problems, plural(loop.MetadataOnly, "source has", "sources have")+" only metadata, no channel text")
		}
		if len(problems) > 0 {
			src.Note = strings.Join(problems, " and ") + ": m365crawl loop sources shows each one, its fix and its retry command"
		}
		if pending := loop.ArchivedRecords - loop.Registered; pending > 0 {
			src.Note = withNote(src.Note, plural(pending, "archived record is", "archived records are")+" not registered: m365crawl loop sync registers new records and names any it cannot register")
		}
		if unscoped != "" {
			src.Note = withNote(src.Note, unscoped)
		}
		src.Note = withNote(src.Note, loopOverviewMeaning)
	}
	return src
}

// loopOverviewMeaning says what the Loop row's time and counts mean, on every row with a
// registered source.
const loopOverviewMeaning = "last_sync_at is the last Loop projection (last_projected_at), not an edit time; channels are partial snapshots of cached text, not full or current pages; " +
	"an archived record is one cached Loop record in the archive, registered as one source; a source holds any number of channels, or none"

// loopOverviewPlain is loopOverviewMeaning for text output, without JSON field names.
const loopOverviewPlain = "the time shown is the last Loop projection, not an edit time; channels are partial snapshots of cached text, not full or current pages; " +
	"an archived record is one cached Loop record in the archive, registered as one source; a source holds any number of channels, or none"

// lastCapture is the newest admitted capture of sources, or since when it is newer.
func lastCapture(sources []store.KnowledgeSource, since time.Time) time.Time {
	for _, s := range sources {
		if s.AdmittedAt != nil && s.AdmittedAt.After(since) {
			since = *s.AdmittedAt
		}
	}
	return since
}

func intPtr(n int) *int { return &n }

// at is t as a JSON time, or nil when it is zero.
func at(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func (rt *runtime) overviewChats(st *store.Store, accountNote string) (overviewSource, error) {
	row, err := st.Status(rt.ctx)
	if err != nil {
		return overviewSource{}, err
	}
	var conv, msgs int
	newest, last := time.Time{}, row.LastSuccessAt
	if rt.account != nil {
		last = time.Time{}
	}
	for _, a := range row.Accounts {
		if rt.account != nil && (a.TenantID != rt.account.TenantID || a.UserID != rt.account.UserID) {
			continue
		}
		conv, msgs = conv+a.Conversations, msgs+a.Messages
		if a.NewestSentAt.After(newest) {
			newest = a.NewestSentAt
		}
		if rt.account != nil {
			last = a.LastSyncedAt
		}
	}
	src := overviewSource{Source: "chats", State: overviewOK, Conversations: &conv, Messages: &msgs, NewestAt: at(newest), LastSyncAt: at(last)}
	if msgs == 0 {
		src.State, src.Note = overviewEmpty, firstOf(accountNote, "no Teams messages archived yet: run m365crawl sync, and m365crawl doctor if it stays empty")
	}
	return src, nil
}

// Mail states of the overview beyond the shared ones: the Outlook source is off for this run, or
// this machine has no Outlook profile.
const (
	overviewOff       = "off"
	overviewNoProfile = "no_profile"
)

// overviewMail says what the archive holds of mail and, when it holds none, why: mail is not read
// on this platform, the Outlook source is off, there is no Outlook profile, it was read and is
// empty, or it was never read. Only the last is fixed by a sync. --account outlook/<profile>
// narrows it to that account; a Teams account leaves it covering every Outlook account.
func (rt *runtime) overviewMail(st *store.Store) (overviewSource, error) {
	account := rt.mailAccount()
	b, err := rt.mailStatusOf(st, account)
	if err != nil {
		return overviewSource{}, err
	}
	src := overviewSource{Source: "mail", State: overviewOK, Messages: &b.Messages, NewestAt: at(b.NewestAt), OldestAt: at(b.OldestAt), LastSyncAt: at(b.SyncedAt)}
	switch {
	case b.SourceError != nil:
		src.State, src.Note = mailStateUnavailable, mailSourceNote(b.SourceError)
	case b.State == mailStateUnsupportedPlatform:
		return overviewSource{Source: "mail", State: overviewUnsupported, Note: "mail is not supported on this operating system"}, nil
	case account != "" && b.Messages == 0:
		src.State, src.Note = overviewEmpty, "no mail is archived for account "+account+": m365crawl mail folders lists the Outlook accounts with mail"
	case b.Messages > 0 && !rt.outlookOn:
		src.Note = "the Outlook source is off for this run, so this archived mail is not being refreshed"
	case b.Messages > 0:
	default:
		src.State, src.Note = rt.whyNoMail(b)
	}
	if rt.account != nil && account == "" { // a Teams account: mail is not narrowed by it
		src.Note = strings.TrimPrefix(src.Note+"; mail is shown for every Outlook account, and --account outlook/<profile> narrows it", "; ")
	}
	return src, nil
}

// noMailYet is the note of mail that was never read, the one cause a sync fixes.
const noMailYet = "no mail has been read yet: run m365crawl sync; m365crawl doctor says why if it stays empty"

// whyNoMail is the state and note of an archive with no mail on a platform that reads it: the
// Outlook source is off, there is no Outlook profile, the read found none, or none was read yet.
func (rt *runtime) whyNoMail(b *mailStatusBlock) (state, note string) {
	switch {
	case b.SourceError != nil:
		return mailStateUnavailable, mailSourceNote(b.SourceError)
	case !rt.outlookOn:
		return overviewOff, "mail is not read in this run: the Outlook source is off (--outlook-root none, or --teams-root without --outlook-root)"
	case b.State == mailStateNoProfile:
		return overviewNoProfile, "no supported Outlook profile on this machine, so there is no mail to read"
	case b.State == mailStateOK:
		return overviewEmpty, "mail was read and the Outlook cache holds no messages"
	}
	return overviewEmpty, noMailYet
}

// calendarAgendaOf is the test seam of the agenda the overview reads today's coverage from.
var calendarAgendaOf = (*store.Store).CalendarAgenda

func (rt *runtime) overviewCalendar(st *store.Store, accountNote string) (overviewSource, error) {
	start, end, events, err := rt.calendarWindow(st)
	if err != nil {
		return overviewSource{}, err
	}
	src := overviewSource{Source: "calendar", State: overviewOK, Events: &events, WindowStart: at(start), WindowEnd: at(end)}
	if start.IsZero() {
		src.State, src.Note = overviewEmpty, firstOf(accountNote, "no calendar archived yet: run m365crawl sync")
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
			when := overviewTime(s.LastSyncAt)
			if s.LastProjectedAt != nil {
				when += " (last projection)"
			}
			rows[i] = []string{s.Source, s.State, overviewHolds(s), when}
		}
		render.Table(w, []string{"source", "state", "holds", "last sync"}, rows, color)
		for _, s := range r.Sources {
			if s.Note != "" {
				_, _ = fmt.Fprintf(w, "%s\n", render.Dim(s.Source+": "+strings.Replace(s.Note, loopOverviewMeaning, loopOverviewPlain, 1), color))
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
	case s.Messages != nil && *s.Messages == 0 && s.Conversations != nil:
		return "no messages"
	case s.Messages != nil && *s.Messages == 0:
		return "no mail"
	case s.Events != nil && *s.Events == 0:
		return "no events"
	case s.Conversations != nil:
		return fmt.Sprintf("%s in %s, newest %s", plural(*s.Messages, "message", "messages"), plural(*s.Conversations, "conversation", "conversations"), overviewTime(s.NewestAt))
	case s.Messages != nil:
		return fmt.Sprintf("%s from %s, newest %s", plural(*s.Messages, "message", "messages"), overviewDay(s.OldestAt), overviewTime(s.NewestAt))
	case s.Channels != nil && *s.Registered == 0:
		return plural(*s.ArchivedRecords, "archived record", "archived records") + "; no sources registered"
	case s.Channels != nil:
		return registeredHolds(*s.Channels, "Loop channel (partial snapshot)", "Loop channels (partial snapshots)", s)
	case s.Registered != nil && *s.Registered == 0 && s.Observations == nil:
		return "no sources registered"
	case s.Documents != nil:
		return registeredHolds(*s.Documents, "document observation", "document observations", s)
	case s.Notes != nil:
		return registeredHolds(*s.Notes, "note", "notes", s)
	case s.People != nil && *s.Registered == 0:
		return plural(*s.People, "person", "people") + " (Teams and mail); no native Outlook people sources"
	case s.People != nil:
		return fmt.Sprintf("%s (Teams and mail); %s from %s",
			plural(*s.People, "person", "people"), plural(*s.Observations, "native Outlook observation", "native Outlook observations"), plural(*s.Registered, "source", "sources"))
	case s.Calls != nil && *s.Calls == 0:
		return "no recorded meetings"
	case s.Calls != nil:
		return fmt.Sprintf("%s, %s, %d fetched", plural(*s.Calls, "recorded meeting", "recorded meetings"), plural(*s.Parts, "transcript part", "transcript parts"), *s.Fetched)
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

// registeredHolds is a registered source's rows, sources and failures in one phrase.
func registeredHolds(rows int, one, many string, s overviewSource) string {
	holds := plural(rows, one, many) + " from " + plural(*s.Registered, "source", "sources")
	if s.Failed != nil && *s.Failed > 0 {
		holds += fmt.Sprintf("; %d failed", *s.Failed)
	}
	return holds
}

// withNote appends a clause to a row's note.
func withNote(note, clause string) string { return strings.TrimPrefix(note+"; "+clause, "; ") }

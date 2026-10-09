package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

// transcriptsGroup is the transcripts command: with no subcommand it lists.
type transcriptsGroup struct {
	List   transcriptsCmd       `cmd:"" default:"withargs" help:"List meetings with recordings and their transcript parts, in order, with what is fetched. Offline: reads only the archive."`
	Show   transcriptsShowCmd   `cmd:"" help:"Print a meeting's transcript from the archive: every part in time order, with each seam marked and where each part came from. Offline."`
	Fetch  transcriptsFetchCmd  `cmd:"" help:"Fetch the transcripts of a meeting's parts from SharePoint and store them, so later reads are offline. Runs an invisible Edge (or Chrome) with m365crawl's own browser profile; m365crawl never sees a password or token."`
	Signin transcriptsSigninCmd `cmd:"" help:"Open m365crawl's browser profile in a visible window once, so you can sign in to SharePoint; transcripts fetch then signs in silently."`
}

const transcriptsCommon = `A recorded meeting is one call. A call has one part for each stretch that was recorded or transcribed, and each part's transcript is a file of its own in SharePoint. The parts are listed from the recording notices in the meeting chat, which a sync already archived; listing and showing them reads only the archive.
<meeting> is an event id or key (as calendar prints them), a meeting chat link or thread id, or a call id. A chat or an event can hold several recorded calls.
A part's state is ok when its text is in the archive, not_fetched when it can be fetched and has not been, unfetchable when no file reference is cached for it, and otherwise the outcome of the last attempt (no_access, not_found, no_transcript, too_large, failed). reason says in one sentence why a part has no text. A meeting's state is fetched, partial, not_fetched or unfetchable.
Every result says where it came from: source is archive, and fetched_at says when each part's text was fetched.`

// Help is the long help of the transcripts group, shared by its commands.
func (transcriptsGroup) Help() string { return transcriptsCommon }

const transcriptsSeeGroup = "What a meeting reference, a part and each state are is in `m365crawl transcripts --help`."

// Help is the long help of the list.
func (transcriptsCmd) Help() string {
	return transcriptsSeeGroup + "\nWithout <meeting> every recorded meeting is listed, newest first, with its part counts. With <meeting> each call of it is listed with its parts. --since and --until take YYYY-MM-DD, RFC3339 or a relative age such as 7d and compare the meeting's start; a date alone as --until includes that whole day."
}

// Help is the long help of transcripts show.
func (transcriptsShowCmd) Help() string {
	return transcriptsSeeGroup + "\nThe transcript is one segment per part, in time order. A part with no text keeps its place as a segment with no entries and its reason, and complete is false. start and end are absolute times; offset is from the start of the part. When <meeting> names several recorded calls the newest is shown and a notice says how to pick another. --max-text cuts each entry's text and sets text_truncated."
}

const maxTranscriptLimit = 1000

// noTranscriptTables is the note and hint of an archive from before the transcript tables.
const noTranscriptTables = "the archive has no transcript tables yet: run m365crawl sync"

const sourceArchive = "archive"

// ---- items ----

type transcriptFetch struct {
	State       string     `json:"state"`
	FetchedAt   *time.Time `json:"fetched_at"`
	AttemptedAt *time.Time `json:"attempted_at"`
	Entries     int        `json:"entries"`
	HTTPStatus  int        `json:"http_status"`
	Browser     *string    `json:"browser"`
}

// transcriptPart is one part of a call. fetch is null until a fetch was attempted; state is the
// part's own state and reason why it has no text (null when it has).
type transcriptPart struct {
	Ordinal         int              `json:"ordinal"`
	PartKey         string           `json:"part_key"`
	StartsAt        *time.Time       `json:"starts_at"`
	DurationSeconds float64          `json:"duration_seconds"`
	TranscribeOnly  bool             `json:"transcribe_only"`
	ContentTypes    string           `json:"content_types"`
	StorageKind     *string          `json:"storage_kind"`
	RefQuality      string           `json:"ref_quality"`
	Fetchable       bool             `json:"fetchable"`
	State           string           `json:"state"`
	Reason          *string          `json:"reason"`
	Fetch           *transcriptFetch `json:"fetch"`
}

// transcriptItem is one recorded call. parts is present only when a meeting was named.
type transcriptItem struct {
	CallID         string           `json:"call_id"`
	ThreadID       string           `json:"thread_id"`
	EventKey       *string          `json:"event_key"`
	Title          *string          `json:"title"`
	StartedAt      *time.Time       `json:"started_at"`
	State          string           `json:"state"`
	PartsTotal     int              `json:"parts_total"`
	PartsFetchable int              `json:"parts_fetchable"`
	PartsFetched   int              `json:"parts_fetched"`
	Parts          []transcriptPart `json:"parts,omitempty"`
}

func transcriptPartOf(p store.TranscriptPart) transcriptPart {
	out := transcriptPart{Ordinal: p.Ordinal, PartKey: p.PartKey, StartsAt: tp(p.StartsAt), DurationSeconds: p.DurationSeconds,
		TranscribeOnly: p.TranscribeOnly, ContentTypes: p.ContentTypes, StorageKind: nz(p.StorageKind), RefQuality: p.RefQuality,
		Fetchable: p.Fetchable, State: p.State(), Reason: nz(p.Reason)}
	if f := p.Fetch; f != nil {
		out.Fetch = &transcriptFetch{State: f.State, FetchedAt: f.FetchedAt, AttemptedAt: f.AttemptedAt, Entries: f.EntryCount, HTTPStatus: f.HTTPStatus, Browser: nz(f.Browser)}
	}
	return out
}

func transcriptItemOf(c store.TranscriptCall, withParts bool) transcriptItem {
	fetchable, fetched := c.Fetchable()
	it := transcriptItem{CallID: c.CallID, ThreadID: c.ThreadID, EventKey: nz(c.EventKey), Title: nz(c.Title), StartedAt: tp(c.StartedAt),
		State: c.State, PartsTotal: len(c.Parts), PartsFetchable: fetchable, PartsFetched: fetched}
	if withParts {
		for _, p := range c.Parts {
			it.Parts = append(it.Parts, transcriptPartOf(p))
		}
	}
	return it
}

// transcriptListResult is the document of the transcripts list.
type transcriptListResult struct {
	Items     []any  `json:"items"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated"`
	Source    string `json:"source"`
	meta
	calls     []store.TranscriptCall // the items, for the text output
	typed     bool                   // the items are transcriptItem (no --fields)
	withParts bool
}

func (rt *runtime) newTranscriptList(calls []store.TranscriptCall, truncated, withParts bool) *transcriptListResult {
	items := make([]transcriptItem, len(calls))
	for i, c := range calls {
		items[i] = transcriptItemOf(c, withParts)
	}
	res := &transcriptListResult{Items: shape(rt, items), Truncated: truncated, Source: sourceArchive, calls: calls, typed: len(rt.fields) == 0, withParts: withParts}
	res.Count = len(res.Items)
	return res
}

// ---- transcripts [<meeting>] ----

type transcriptsCmd struct {
	Meeting string `arg:"" optional:"" help:"Only this meeting, with its parts: an event id or key, a meeting chat link or thread id, or a call id."`
	Since   string `help:"Only meetings that started at or after this time (YYYY-MM-DD, RFC3339 or an age such as 7d)." placeholder:"DATE"`
	Until   string `help:"Only meetings that started before this time; a date alone (YYYY-MM-DD) includes that whole day." placeholder:"DATE"`
	State   string `help:"Only meetings in this state: fetched (every part that can be fetched is in the archive), partial, not_fetched or unfetchable (no part can be fetched)." placeholder:"STATE"`
	Limit   int    `default:"50" help:"Maximum meetings to return (at most 1000); truncated says whether more exist." placeholder:"N"`
}

var transcriptStates = []string{store.CallFetched, store.CallPartial, store.CallNotFetched, store.CallUnfetchable}

// meetingRef turns what the user typed into what the archive resolves: a Teams link (a meeting
// link, or a link to the meeting chat or one of its messages) names that chat; anything else is
// passed on as it is.
func meetingRef(arg string) (string, error) {
	if !isLink(arg) {
		return arg, nil
	}
	thread, _, _, err := parseTeamsLink(arg)
	if err != nil {
		c := err.coded("Pass a link like https://teams.microsoft.com/l/message/<threadId>/<messageId>, or a thread id, a call id or an event id.")
		c.Message = fmt.Sprintf("%q is not a link to a meeting or its chat: %s", arg, err.why)
		return "", c
	}
	return thread, nil
}

func (c *transcriptsCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "transcripts", ""); err != nil {
		return err
	}
	if c.Meeting == "" && contains(rt.fields, "parts") {
		u := errs.Usage("--fields key \"parts\" needs a <meeting>: the list of every meeting carries counts, not parts")
		u.Fix = "Run `m365crawl transcripts <meeting> --fields call_id,parts`, taking the call_id from the list."
		return u
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	if c.Limit > maxTranscriptLimit {
		u := errs.Usage(fmt.Sprintf("--limit %d is more than transcripts lists at once", c.Limit))
		u.Fix = fmt.Sprintf("Use --limit %d or less, and narrow the list with --since, --until or --state.", maxTranscriptLimit)
		return u
	}
	if c.State != "" && !contains(transcriptStates, c.State) {
		u := errs.Usage(fmt.Sprintf("--state %q is not a state; the states are %s", c.State, strings.Join(transcriptStates, ", ")))
		u.Fix = "Pick one of the states in the message."
		return u
	}
	since, err := rt.when("--since", c.Since)
	if err != nil {
		return err
	}
	until, err := rt.when("--until", c.Until)
	if err != nil {
		return err
	}
	if dateOnly.MatchString(c.Until) {
		until = until.In(time.Local).AddDate(0, 0, 1) // a date alone names the whole day
	}
	ref, err := meetingRef(c.Meeting)
	if err != nil {
		return err
	}
	return rt.read("transcripts", func(st *store.Store) (result, error) {
		withParts := c.Meeting != ""
		if st == nil {
			res := rt.newTranscriptList(nil, false, withParts)
			res.Note = "no archive yet: run m365crawl sync"
			return res, nil
		}
		if ok, err := st.HasTranscriptTables(rt.ctx); err != nil || !ok {
			res := rt.newTranscriptList(nil, false, withParts)
			res.Note = noTranscriptTables
			res.setNeedsSync(noTranscriptTables)
			return res, err
		}
		f := store.TranscriptFilter{Account: rt.account, Since: since, Until: until, State: c.State, Limit: c.Limit}
		if withParts {
			if f.Calls, _, err = st.ResolveMeeting(rt.ctx, rt.account, ref); err != nil {
				return nil, err
			}
		}
		calls, truncated, err := transcriptCallsOf(st, rt.ctx, f)
		if err != nil {
			return nil, err
		}
		res := rt.newTranscriptList(calls, truncated, withParts)
		if len(calls) == 0 {
			filtered := withParts || !since.IsZero() || !until.IsZero() || c.State != ""
			if res.Note, err = rt.transcriptsEmptyNote(st, filtered); err != nil {
				return nil, err
			}
		}
		if n := unfetchedCalls(calls); n > 0 {
			res.addNotice(fmt.Sprintf("%s transcript parts that are not fetched yet; run m365crawl transcripts fetch <call-id>", ofListed(n, len(calls))))
		}
		return res, nil
	})
}

// ofListed is the notice's subject and verb: which of the listed meetings have unfetched parts.
func ofListed(n, total int) string {
	switch {
	case total == 1:
		return "this meeting has"
	case n == 1:
		return fmt.Sprintf("1 of the %d meetings listed has", total)
	}
	return fmt.Sprintf("%d of the %d meetings listed have", n, total)
}

// unfetchedCalls counts the calls with a part a fetch can ask for and has not brought back.
func unfetchedCalls(calls []store.TranscriptCall) int {
	n := 0
	for _, c := range calls {
		if c.State == store.CallPartial || c.State == store.CallNotFetched {
			n++
		}
	}
	return n
}

// transcriptCallsOf is the test seam of the archive's recorded calls.
var transcriptCallsOf = (*store.Store).TranscriptCalls

// Test seams of the two questions search, status and calendar event ask before they read
// transcripts: whether the archive has the tables, and whether it holds any text.
var (
	transcriptTablesOf = (*store.Store).HasTranscriptTables
	transcriptTextOf   = (*store.Store).HasTranscriptText
)

// transcriptsEmptyNote says why the list is empty: an account the archive does not hold, an
// archive with no recorded meeting, or filters that matched none.
func (rt *runtime) transcriptsEmptyNote(st *store.Store, filtered bool) (string, error) {
	if note, err := rt.unknownAccountNote(st); note != "" || err != nil {
		return note, err
	}
	const none = "no recorded meetings in the archive yet; run m365crawl sync"
	if !filtered {
		return none, nil
	}
	any, _, err := transcriptCallsOf(st, rt.ctx, store.TranscriptFilter{Account: rt.account, Limit: 1})
	if err != nil {
		return "", err
	}
	if len(any) == 0 {
		return none, nil
	}
	return "no recorded meeting matched the filters", nil
}

// ---- transcripts show ----

type transcriptsShowCmd struct {
	Meeting string `arg:"" help:"An event id or key, a meeting chat link or thread id, or a call id."`
}

type transcriptEntry struct {
	Speaker       *string    `json:"speaker"`
	Start         *time.Time `json:"start"`
	End           *time.Time `json:"end"`
	Offset        *string    `json:"offset"`
	Text          string     `json:"text"`
	TextTruncated bool       `json:"text_truncated,omitempty"`
}

type transcriptSegment struct {
	Ordinal        int               `json:"ordinal"`
	TranscribeOnly bool              `json:"transcribe_only"`
	StartsAt       *time.Time        `json:"starts_at"`
	FetchedAt      *time.Time        `json:"fetched_at"`
	State          string            `json:"state"`
	Reason         *string           `json:"reason"`
	Entries        []transcriptEntry `json:"entries"`
}

// transcriptShow is one call's transcript. complete says every part's text is in the archive.
type transcriptShow struct {
	CallID        string              `json:"call_id"`
	EventKey      *string             `json:"event_key"`
	Title         *string             `json:"title"`
	Source        string              `json:"source"`
	Complete      bool                `json:"complete"`
	Segments      []transcriptSegment `json:"segments"`
	TextTruncated bool                `json:"text_truncated,omitempty"`
}

// transcriptShowResult is transcripts show's document: the transcript, or only the keys --fields
// kept, then the archive's age like every read.
type transcriptShowResult struct {
	show *transcriptShow
	keys []string
	meta
	startedAt time.Time // for the text output
}

// MarshalJSON prints the transcript's keys (all, or those --fields asked for, in that order), then
// the meta keys.
func (r *transcriptShowResult) MarshalJSON() ([]byte, error) {
	var body any = r.show
	if r.show == nil {
		body = struct{}{}
	} else if len(r.keys) > 0 {
		body, _ = project(r.show, append(append([]string(nil), r.keys...), "text_truncated")) // a transcript always encodes
	}
	return joinJSON(body, r.meta)
}

// offsetText is an offset from the start of a part as h:mm:ss.
func offsetText(ms int64) string {
	s := ms / 1000
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

func transcriptEntryOf(e transcripts.Entry, partStart time.Time, max int) transcriptEntry {
	out := transcriptEntry{Speaker: nz(e.Speaker), Text: e.Text}
	if t, did := truncateRunes(e.Text, max); did {
		out.Text, out.TextTruncated = t, true
	}
	abs := func(ms *int64) *time.Time {
		if ms == nil || partStart.IsZero() {
			return nil
		}
		return tp(partStart.Add(time.Duration(*ms) * time.Millisecond))
	}
	out.Start, out.End = abs(e.StartMS), abs(e.EndMS)
	if e.StartMS != nil {
		o := offsetText(*e.StartMS)
		out.Offset = &o
	}
	return out
}

func (c *transcriptsShowCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "transcripts show", ""); err != nil {
		return err
	}
	ref, err := meetingRef(c.Meeting)
	if err != nil {
		return err
	}
	return rt.read("transcripts show", func(st *store.Store) (result, error) {
		if st == nil {
			return &transcriptShowResult{}, nil // no archive yet: read adds needs_sync and the hint
		}
		if ok, err := st.HasTranscriptTables(rt.ctx); err != nil || !ok {
			r := &transcriptShowResult{}
			r.setNeedsSync(noTranscriptTables)
			return r, err
		}
		ids, kind, err := st.ResolveMeeting(rt.ctx, rt.account, ref)
		if err != nil {
			return nil, err
		}
		calls, _, err := transcriptCallsOf(st, rt.ctx, store.TranscriptFilter{Account: rt.account, Calls: ids[:1]})
		if err != nil {
			return nil, err
		}
		call := calls[0] // ResolveMeeting names only calls that have parts
		parts := make([]transcripts.StitchPart, len(call.Parts))
		for i, p := range call.Parts {
			parts[i] = transcripts.StitchPart{Part: p.Part}
			if p.Fetch != nil {
				parts[i].State, parts[i].FetchedAt = p.Fetch.State, p.Fetch.FetchedAt
			}
			if p.HasText() {
				if parts[i].Entries, err = st.TranscriptEntries(rt.ctx, call.AccountID, p.PartKey); err != nil {
					return nil, err
				}
			}
		}
		show := &transcriptShow{CallID: call.CallID, EventKey: nz(call.EventKey), Title: nz(call.Title), Source: sourceArchive, Complete: true, Segments: []transcriptSegment{}}
		for _, seg := range transcripts.Stitch(parts) {
			out := transcriptSegment{Ordinal: seg.Ordinal, TranscribeOnly: seg.TranscribeOnly, StartsAt: tp(seg.StartsAt), FetchedAt: seg.FetchedAt,
				State: seg.State, Reason: nz(seg.Reason), Entries: []transcriptEntry{}}
			for _, e := range seg.Entries {
				eo := transcriptEntryOf(e, seg.StartsAt, rt.g.MaxText)
				show.TextTruncated = show.TextTruncated || eo.TextTruncated
				out.Entries = append(out.Entries, eo)
			}
			show.Complete = show.Complete && seg.State == transcripts.StateOK
			show.Segments = append(show.Segments, out)
		}
		res := &transcriptShowResult{show: show, keys: rt.fields, startedAt: call.StartedAt}
		if len(ids) > 1 {
			what := "meeting"
			if kind == store.MeetingByThread {
				what = "chat"
			}
			res.addNotice(fmt.Sprintf("this %s has %d recorded calls; pass a call id from: m365crawl transcripts %s", what, len(ids), shellWord(ref)))
		}
		return res, nil
	})
}

// ---- text ----

// transcriptRenderer is a transcripts result that prints itself as text.
type transcriptRenderer interface {
	renderTranscripts(rt *runtime)
}

func (r *transcriptListResult) renderTranscripts(rt *runtime) {
	w := rt.stdout
	switch {
	case !r.typed || len(r.calls) == 0:
		rt.listTable(&listResult{Items: r.Items})
	case r.withParts:
		for i, c := range r.calls {
			if i > 0 {
				_, _ = fmt.Fprintln(w)
			}
			rt.callBlock(c)
		}
	default:
		rows := make([][]string, len(r.calls))
		for i, c := range r.calls {
			fetchable, fetched := c.Fetchable()
			rows[i] = []string{stamp(c.StartedAt), c.State, strconv.Itoa(len(c.Parts)), fmt.Sprintf("%d of %d", fetched, fetchable), oneLine(c.Title), c.CallID}
		}
		rt.fitTable([]string{"started", "state", "parts", "fetched", "title", "call"}, rows, 4, 5)
	}
	_, _ = fmt.Fprintln(w)
	more := ""
	if r.Truncated {
		more = " (more exist; raise --limit)"
	}
	_, _ = fmt.Fprintf(w, "%s\n", render.Dim(fmt.Sprintf("%d items%s", r.Count, more), rt.color))
	metaLines(w, r.meta, rt.color)
	_, _ = fmt.Fprintf(w, "%s\n", render.Dim("source: "+r.Source, rt.color))
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(displayZone).Format("15:04")
}

// callBlock prints one call and the table of its parts.
func (rt *runtime) callBlock(c store.TranscriptCall) {
	fetchable, fetched := c.Fetchable()
	_, _ = fmt.Fprintf(rt.stdout, "%s · %s · call %s · %s (%d of %d fetchable parts fetched)\n", firstOf(oneLine(c.Title), "(untitled)"), stamp(c.StartedAt), c.CallID, c.State, fetched, fetchable)
	rows := make([][]string, len(c.Parts))
	for i, p := range c.Parts {
		kind, entries, when := "recorded", "", ""
		switch {
		case p.RefQuality == transcripts.RefUnresolved: // only a transcript notice: no evidence either way
			kind = "unknown"
		case p.TranscribeOnly:
			kind = "transcribe only"
		}
		if p.HasText() {
			entries, when = strconv.Itoa(p.Fetch.EntryCount), stamp(*p.Fetch.FetchedAt)
		}
		rows[i] = []string{strconv.Itoa(p.Ordinal), clock(p.StartsAt), kind, p.State(), entries, when, p.Reason}
	}
	render.Table(rt.stdout, []string{"part", "starts", "kind", "state", "entries", "fetched", "why no text"}, rows, rt.color)
}

// seamLine is the line before every part of a shown transcript.
func seamLine(seg transcriptSegment, of int) string {
	s := fmt.Sprintf("── part %d of %d", seg.Ordinal, of)
	if seg.TranscribeOnly {
		s += " · transcribe only"
	}
	at := time.Time{}
	if seg.StartsAt != nil {
		at = *seg.StartsAt
	}
	s += " · " + clock(at)
	switch {
	case seg.FetchedAt != nil:
		s += " · fetched " + stamp(*seg.FetchedAt)
	case seg.State == transcripts.StateNotFetched: // the reason already says it is not fetched yet
		s += " · " + sv(seg.Reason)
	default:
		s += " · not fetched: " + sv(seg.Reason)
	}
	return s + " ──"
}

func (r *transcriptShowResult) renderTranscripts(rt *runtime) {
	w := rt.stdout
	if r.show == nil {
		metaLines(w, r.meta, rt.color)
		return
	}
	if len(r.keys) > 0 {
		p, _ := project(r.show, append(append([]string(nil), r.keys...), "text_truncated")) // a transcript always encodes
		rt.listTable(&listResult{Items: []any{p}})
		metaLines(w, r.meta, rt.color)
		_, _ = fmt.Fprintf(w, "%s\n", render.Dim("source: "+r.show.Source, rt.color))
		return
	}
	_, _ = fmt.Fprintf(w, "%s · %s · call %s\n", firstOf(oneLine(sv(r.show.Title)), "(untitled)"), stamp(r.startedAt), r.show.CallID)
	fetched := 0
	for _, seg := range r.show.Segments {
		_, _ = fmt.Fprintf(w, "%s\n", render.Dim(seamLine(seg, len(r.show.Segments)), rt.color))
		if seg.FetchedAt != nil {
			fetched++
		}
		// Consecutive entries of one speaker read as one turn.
		for i := 0; i < len(seg.Entries); {
			e := seg.Entries[i]
			texts := []string{oneLine(e.Text)}
			j := i + 1
			for ; j < len(seg.Entries) && sv(seg.Entries[j].Speaker) == sv(e.Speaker); j++ {
				texts = append(texts, oneLine(seg.Entries[j].Text))
			}
			_, _ = fmt.Fprintf(w, "%s %s: %s\n", render.Dim("["+firstOf(sv(e.Offset), "-")+"]", rt.color), firstOf(sv(e.Speaker), "(unknown speaker)"), strings.Join(texts, " "))
			i = j
		}
	}
	_, _ = fmt.Fprintln(w)
	if r.show.TextTruncated {
		_, _ = fmt.Fprintf(w, "%s\n", render.Dim("entries cut by --max-text", rt.color))
	}
	metaLines(w, r.meta, rt.color)
	_, _ = fmt.Fprintf(w, "%s\n", render.Dim(fmt.Sprintf("source: %s · %d of %d parts fetched", r.show.Source, fetched, len(r.show.Segments)), rt.color))
}

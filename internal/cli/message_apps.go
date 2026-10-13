package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ourostack/m365crawl/internal/botcitations"
	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
)

// appCitationPreview is how many admitted citations a message item carries; the full list is in the
// archive (see fullInspection).
const appCitationPreview = 100

// applicationCurrent is the message's current source revision: the source sender, whether raw
// JSON was archived and the SHA256 of its exact bytes (null when raw is absent or its digest is not
// derived yet), and the active mapper version.
type applicationCurrent struct {
	SenderID      string  `json:"sender_id"`
	RawPresent    bool    `json:"raw_present"`
	RawDigest     *string `json:"raw_digest"`
	MapperVersion int     `json:"mapper_version"`
}

// applicationCitationItem is one admitted native citation. id is the native integer id as a
// decimal string; link and content_type keep null apart from an empty string.
type applicationCitationItem struct {
	Position             int     `json:"position"`
	ID                   string  `json:"id"`
	Title                string  `json:"title"`
	TitleTruncated       bool    `json:"title_truncated,omitempty"`
	Link                 *string `json:"link"`
	ContentType          *string `json:"content_type"`
	NestedContentPresent bool    `json:"nested_content_present"`
}

// applicationPayloadItem is the bounded claims of a mapped or partial representation.
type applicationPayloadItem struct {
	AppID               *string                   `json:"app_id"`
	AppName             *string                   `json:"app_name"`
	AppNameTruncated    bool                      `json:"app_name_truncated,omitempty"`
	ReplyToID           *string                   `json:"reply_to_id"`
	Citations           []applicationCitationItem `json:"citations"`
	CitationsCount      int                       `json:"citations_count"`
	TotalAdmitted       int                       `json:"total_admitted"`
	CitationsTruncated  bool                      `json:"citations_truncated"`
	SourceSlots         *int                      `json:"source_slots"`
	RefusedCitationRows int                       `json:"refused_citation_rows"`
	Losses              []applicationLossItem     `json:"losses"`
	FullInspection      string                    `json:"full_inspection"`
}

// applicationLossItem is one kind of native field that could not be read, how many times, and what
// the code means in one line.
type applicationLossItem struct {
	Code    string `json:"code"`
	Count   int    `json:"count"`
	Meaning string `json:"meaning,omitempty"`
}

// applicationSummary is --fields app_summary: the claims an agent most often wants, without the
// nested observation. citations_total is every citation read from the message (served.total_admitted),
// not the at most 100 an item shows (served.citations_count). It is null, like the other claims,
// when nothing is served (not_derived, or failed with nothing retained): unknown, not zero.
type applicationSummary struct {
	State          string  `json:"state"`
	AppName        *string `json:"app_name"`
	AppID          *string `json:"app_id"`
	ReplyToID      *string `json:"reply_to_id"`
	CitationsTotal *int    `json:"citations_total"`
}

// summary is the item's compact form, with the same clipped app name.
func (it *applicationItem) summary() *applicationSummary {
	out := &applicationSummary{State: it.State}
	switch {
	case it.Served != nil && it.Served.applicationPayloadItem != nil:
		p := it.Served.applicationPayloadItem
		out.AppName, out.AppID, out.ReplyToID, out.CitationsTotal = p.AppName, p.AppID, p.ReplyToID, &p.TotalAdmitted
	case it.State == store.AppStateAbsent: // read, and it holds no claim
		out.CitationsTotal = new(int)
	}
	return out
}

// appSummaryCell is the text-output cell of one app_summary, given as its JSON; empty when it
// cannot be read.
func appSummaryCell(raw []byte) string {
	var s applicationSummary
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	if s.CitationsTotal == nil {
		return s.State + "; nothing served"
	}
	parts := []string{s.State}
	switch {
	case s.AppName != nil && s.AppID != nil:
		parts = append(parts, fmt.Sprintf("app %q (id %q)", appText(*s.AppName), appText(*s.AppID)))
	case s.AppName != nil:
		parts = append(parts, fmt.Sprintf("app %q", appText(*s.AppName)))
	case s.AppID != nil:
		parts = append(parts, fmt.Sprintf("app id %q", appText(*s.AppID)))
	default:
		parts = append(parts, "no app named")
	}
	if s.ReplyToID != nil {
		parts = append(parts, fmt.Sprintf("reply to %q", appText(*s.ReplyToID)))
	}
	return strings.Join(append(parts, plural(*s.CitationsTotal, "citation", "citations")), "; ")
}

// appSummaryField is the --fields key that adds app_summary to each returned message.
const appSummaryField = "app_summary"

// withAppSummary sets app_summary on items when --fields asks for it; it is never printed unasked.
func (rt *runtime) withAppSummary(items []messageItem) []messageItem {
	if !contains(rt.fields, appSummaryField) {
		return items
	}
	for i := range items {
		if items[i].App != nil {
			items[i].AppSummary = items[i].App.summary()
		}
	}
	return items
}

// appFailureMeanings explains, in one line each, every failure_code application evidence can
// carry. With botcitations.LossMeanings it is the one table the help and each message's meaning
// read; a test refuses a code that is emitted without a line.
var appFailureMeanings = map[string]string{
	"teams_app_raw_missing":          "the archive holds no raw JSON for this message revision",
	"teams_app_raw_too_large":        "the raw JSON is larger than 8 MiB, or nests or repeats beyond the reader's bounds",
	"teams_app_raw_invalid":          "the raw JSON is not one well-formed JSON object, or an object in it repeats a key",
	"teams_app_identity_unqualified": "the message id or sender id is empty or not valid UTF-8, so its claims cannot be attributed",
	"teams_bot_input_unsupported":    "the mapper was given no message, message id or sender id it can read",
	"teams_bot_input_too_large":      "an app, reply or citation field is longer than 256 KiB, the claims total more than 4 MiB, or the message has more than 4096 citation rows",
	store.AppProjectionCorrupt:       "the stored observation failed validation and is not shown; the message itself is unaffected",
}

// lossMeaning is the one-line meaning of a loss code, empty for a code this version does not know
// (evidence stored by a newer version).
func lossMeaning(code string) string { return botcitations.LossMeanings[code] }

// explained appends "code: meaning" to text when the code has one.
func explained(text, code, meaning string) string {
	if meaning == "" {
		return text
	}
	return text + "; " + code + ": " + meaning
}

// applicationServed is the representation a read serves, with its own source sender, raw digest,
// mapper version and first derivation time (not a source refresh or edit time).
type applicationServed struct {
	ObservationID string    `json:"observation_id"`
	MapperVersion int       `json:"mapper_version"`
	State         string    `json:"state"`
	FailureCode   string    `json:"failure_code,omitempty"`
	SenderID      string    `json:"sender_id"`
	RawPresent    bool      `json:"raw_present"`
	RawDigest     *string   `json:"raw_digest"`
	DerivedAt     time.Time `json:"derived_at"`
	*applicationPayloadItem
}

// applicationItem is a Teams message's app_observation: native application, reply and citation
// evidence of its archived raw JSON. Every claim is the message's own, not a verified bot identity.
type applicationItem struct {
	State           string             `json:"state"`
	FailureCode     string             `json:"failure_code,omitempty"`
	MapperVersion   int                `json:"mapper_version"`
	BackfillPending bool               `json:"backfill_pending"`
	Current         applicationCurrent `json:"current"`
	Served          *applicationServed `json:"served"`
	Retained        bool               `json:"retained"`
	RetainedReason  string             `json:"retained_reason,omitempty"`
	Meaning         string             `json:"meaning,omitempty"`
	TextTruncated   bool               `json:"text_truncated,omitempty"`
}

// applicationCoverage is a result's application_evidence_coverage: the evidence states of the Teams
// messages it returned, outside --fields shaping. It is not application, service or Copilot
// completeness, and zero returned messages make it unknown, not empty.
type applicationCoverage struct {
	Scope            string         `json:"scope"`
	State            string         `json:"state"`
	MapperVersion    int            `json:"mapper_version"`
	ReturnedMessages int            `json:"returned_messages"`
	States           map[string]int `json:"states"`
	// PostedByApp counts the returned messages whose served evidence names an app, by app_id or
	// app_name: mapped only says the claims were read. It is a lower bound when unserved > 0.
	PostedByApp     int    `json:"posted_by_app"`
	Retained        int    `json:"retained"`
	BackfillPending bool   `json:"backfill_pending"`
	Note            string `json:"note"`
	// unserved counts the returned messages that serve nothing (not_derived, or failed with nothing
	// retained), so whether they name an app is unknown.
	unserved int
}

const (
	appCoverageScope = "returned_teams_messages"
	appSyncRemedy    = "run m365crawl sync; reads with --max-age 0 never derive it"
)

var appStates = []string{store.AppStateMapped, store.AppStatePartial, store.AppStateAbsent, store.AppStateFailed, store.AppStateNotDerived}

// appMeaning is the one-sentence meaning of a state an agent should not misread; absent and mapped
// need none beyond the coverage note.
func appMeaning(env store.MessageApplicationEnvelope) string {
	switch {
	case env.State == store.AppStateNotDerived:
		return "not derived for this raw revision yet; " + appSyncRemedy
	case env.Retained:
		return explained("the current raw revision failed; served is the last good observation of this message, from mapper version "+strconv.Itoa(env.Served.Revision.MapperVersion),
			env.FailureCode, appFailureMeanings[env.FailureCode])
	case env.FailureCode == store.AppProjectionCorrupt:
		return appFailureMeanings[store.AppProjectionCorrupt]
	case env.State == store.AppStateFailed:
		return explained("the raw revision could not be read as application evidence; a sync does not change that, and the message itself is unaffected",
			env.FailureCode, appFailureMeanings[env.FailureCode])
	case env.State == store.AppStatePartial:
		text := "some native fields could not be read; losses counts them, and older revisions are not merged in"
		for _, l := range env.Served.Payload.Losses {
			text = explained(text, l.Code, lossMeaning(l.Code))
		}
		return text
	}
	return ""
}

func digestOrNil(d string) *string {
	if d == "" {
		return nil
	}
	return &d
}

// fullInspection is the archive-only command that prints an observation's stored projection: a
// JSON string (decode it again), not the raw message. It selects by rowid, the observation id; a
// representation is never updated or deleted, so the rowid always names the same projection.
// It is a fixed template around the generated positive row id and the archive path db, which it
// names with --db so the command reads the archive the result came from; never source text.
func fullInspection(id int64, db string) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf(`m365crawl --db %s --max-age 0 sql "select projection_json from message_app_observations where rowid=%d" --limit 1`, quoteArg(db), id)
}

// quoteArg quotes the archive path in a printed follow-up for this platform's shells.
var quoteArg = quoteFor(goruntime.GOOS)

// argSafe is the characters a path may hold and still be one bare word in every supported shell.
const argSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:@%+=,-"

// quoteFor quotes one command word for goos: bare when it holds only safe characters, else in
// double quotes on Windows (cmd.exe rejects single quotes; a Windows path cannot contain '"') and
// POSIX single quotes elsewhere.
func quoteFor(goos string) func(string) string {
	if goos == "windows" {
		return func(s string) string {
			if s != "" && strings.Trim(s, argSafe+`\`) == "" {
				return s
			}
			return `"` + s + `"`
		}
	}
	return func(s string) string {
		if s != "" && strings.Trim(s, argSafe) == "" {
			return s
		}
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
}

// getwd is os.Getwd; tests make it fail.
var getwd = os.Getwd

// archiveRef is the archive path a printed follow-up command names: absolute, so it reads the
// same archive from any directory.
func (rt *runtime) archiveRef() string {
	if filepath.IsAbs(rt.dbPath) {
		return rt.dbPath
	}
	wd, err := getwd()
	if err != nil {
		return rt.dbPath
	}
	return filepath.Join(wd, rt.dbPath)
}

// applicationItemOf builds a message's app_observation, clipping descriptive text to maxText runes.
func applicationItemOf(env store.MessageApplicationEnvelope, maxText int, db string) applicationItem {
	it := applicationItem{State: env.State, FailureCode: env.FailureCode, MapperVersion: env.ActiveMapperVersion, BackfillPending: env.BackfillPending,
		Current:  applicationCurrent{SenderID: env.Current.SenderID, RawPresent: env.Current.RawPresent, RawDigest: digestOrNil(env.Current.RawDigest), MapperVersion: env.Current.MapperVersion},
		Retained: env.Retained, RetainedReason: env.RetainedReason, Meaning: appMeaning(env)}
	if rep := env.Served; rep != nil {
		it.Served = &applicationServed{ObservationID: "message-app-observation:" + strconv.FormatInt(rep.ID, 10), MapperVersion: rep.Revision.MapperVersion,
			State: rep.State, FailureCode: rep.FailureCode, SenderID: rep.Revision.SenderID, RawPresent: rep.Revision.RawPresent,
			RawDigest: digestOrNil(rep.Revision.RawDigest), DerivedAt: rep.DerivedAt.UTC()}
		if rep.State == store.AppStateMapped || rep.State == store.AppStatePartial {
			it.Served.applicationPayloadItem = payloadItemOf(rep, db)
		}
	}
	it.truncate(maxText)
	return it
}

func payloadItemOf(rep *store.ApplicationRepresentation, db string) *applicationPayloadItem {
	p := rep.Payload
	out := &applicationPayloadItem{AppID: p.AppID, AppName: p.AppName, ReplyToID: p.ReplyToID, Citations: []applicationCitationItem{},
		TotalAdmitted: len(p.Citations), SourceSlots: p.SourceSlots, Losses: []applicationLossItem{}, FullInspection: fullInspection(rep.ID, db)}
	for i, c := range p.Citations {
		if i == appCitationPreview {
			out.CitationsTruncated = true
			break
		}
		out.Citations = append(out.Citations, applicationCitationItem{Position: c.Position, ID: c.ID, Title: c.Title, Link: c.Link, ContentType: c.ContentType, NestedContentPresent: c.NestedContentPresent})
	}
	out.CitationsCount = len(out.Citations)
	for _, l := range p.Losses {
		out.Losses = append(out.Losses, applicationLossItem{Code: l.Code, Count: l.Count, Meaning: lossMeaning(l.Code)})
		if l.Code == "teams_bot_citation_unmapped" {
			out.RefusedCitationRows = l.Count
		}
	}
	return out
}

// truncate clips the served app name and citation titles to max runes and reports whether any
// descriptive text of the item is clipped. It is idempotent: text already clipped is left alone, so
// calendar's later clipping never cuts twice, and max <= 0 changes nothing. Ids and links are never
// clipped.
func (it *applicationItem) truncate(max int) bool {
	if max <= 0 || it.Served == nil || it.Served.applicationPayloadItem == nil {
		return it.TextTruncated
	}
	p := it.Served.applicationPayloadItem
	if p.AppName != nil && !p.AppNameTruncated {
		if t, did := truncateRunes(*p.AppName, max); did {
			p.AppName, p.AppNameTruncated, it.TextTruncated = &t, true, true
		}
	}
	for i := range p.Citations {
		c := &p.Citations[i]
		if t, did := truncateRunes(c.Title, max); did && !c.TitleTruncated {
			c.Title, c.TitleTruncated, it.TextTruncated = t, true, true
		}
	}
	return it.TextTruncated
}

// applicationCoverageOf summarizes the evidence of the Teams messages a result returned.
func applicationCoverageOf(envs []store.MessageApplicationEnvelope) *applicationCoverage {
	c := &applicationCoverage{Scope: appCoverageScope, State: "observed", MapperVersion: store.ActiveApplicationVersion(), ReturnedMessages: len(envs), States: map[string]int{}}
	for _, s := range appStates {
		c.States[s] = 0
	}
	for _, env := range envs {
		c.MapperVersion = env.ActiveMapperVersion // every envelope of one read shares it
		c.States[env.State]++
		if env.Retained {
			c.Retained++
		}
		switch {
		case namesApp(env.Served):
			c.PostedByApp++
		case env.State == store.AppStateNotDerived, env.State == store.AppStateFailed && !env.Retained:
			c.unserved++
		}
		c.BackfillPending = c.BackfillPending || env.BackfillPending
	}
	switch {
	case len(envs) == 0:
		c.State, c.Note = "unknown", "no Teams message was returned, so application evidence is unknown here, not absent"
	default:
		c.Note = "counts cover only the returned Teams messages; app, reply and citation fields are each message's own native claims, not verified bot identity, Copilot history or resolved documents; " +
			"posted_by_app counts the returned messages whose served evidence names an app by app_id or app_name, and mapped means the claims were read, not that an app or bot posted the message"
	}
	if c.unserved > 0 {
		c.Note += "; posted_by_app is a lower bound: " + plural(c.unserved, "returned message is", "returned messages are") + " not_derived or failed with nothing served, so whether they name an app is unknown"
	}
	if c.States[store.AppStateNotDerived] > 0 {
		c.Note += "; not_derived messages have no evidence yet: " + appSyncRemedy
	}
	return c
}

// namesApp reports whether a served representation names an app, by a non-empty app id or name:
// what the text marker shows as [app: NAME] or [app id: ID].
func namesApp(rep *store.ApplicationRepresentation) bool {
	if rep == nil {
		return false
	}
	return (rep.Payload.AppID != nil && *rep.Payload.AppID != "") || (rep.Payload.AppName != nil && *rep.Payload.AppName != "")
}

func envelopesOf(rows []store.MessageRow) []store.MessageApplicationEnvelope {
	out := make([]store.MessageApplicationEnvelope, len(rows))
	for i, r := range rows {
		out[i] = r.Application
	}
	return out
}

// appText makes source text safe for one terminal line: whitespace runs collapse and control
// characters are replaced, so a native claim cannot move the cursor or recolor the terminal.
func appText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, oneLine(s))
}

// appMarkerName is how many terminal columns of an app name or id the text marker keeps, so the
// marker stays short enough to print whole on every row.
const appMarkerName = 24

// markerName is an app name or id cut to appMarkerName columns, with the three dots the table uses
// for a cut cell, also where --max-text already cut the name.
func markerName(s string) string {
	s = appText(s)
	if cut := strings.TrimSuffix(s, "…"); cut != s {
		s = cut + "..."
	}
	return render.Truncate(s, appMarkerName)
}

// appMarker is the short tag a message's text column starts with when it carries served
// application evidence; empty otherwise. The table never clips it.
func appMarker(it *applicationItem) string {
	if it == nil || it.Served == nil || it.Served.applicationPayloadItem == nil {
		return ""
	}
	tag := "app evidence"
	switch p := it.Served.applicationPayloadItem; {
	case p.AppName != nil && *p.AppName != "":
		tag = "app: " + markerName(*p.AppName)
	case p.AppID != nil && *p.AppID != "":
		tag = "app id: " + markerName(*p.AppID)
	}
	if it.Retained {
		tag += ", retained"
	}
	return "[" + tag + "] "
}

// appDetailLines describe one message's application evidence in text output: what the source
// claims, its citations and how to read the full record. Absent and not-derived evidence prints
// nothing here; the coverage line counts it.
func appDetailLines(where string, it *applicationItem) []string {
	if it == nil || it.State == store.AppStateAbsent || it.State == store.AppStateNotDerived {
		return nil
	}
	head := where + ": application evidence " + it.State
	if it.FailureCode != "" {
		head += " (" + it.FailureCode + ")"
	}
	if it.Meaning != "" {
		head += "; " + it.Meaning
	}
	lines := []string{head}
	if it.Served == nil || it.Served.applicationPayloadItem == nil {
		return lines
	}
	p := it.Served.applicationPayloadItem
	var claims, app []string
	if p.AppName != nil {
		app = append(app, fmt.Sprintf("app %q", appText(*p.AppName)))
	}
	if p.AppID != nil {
		app = append(app, fmt.Sprintf("app id %q", appText(*p.AppID)))
	}
	if len(app) > 0 {
		claims = append(claims, strings.Join(app, ", ")+", claimed by the message itself, not a verified bot identity")
	}
	if p.ReplyToID != nil {
		claims = append(claims, fmt.Sprintf("native reply-to id %q (not a verified thread root)", appText(*p.ReplyToID)))
	}
	if len(claims) > 0 {
		lines = append(lines, "  "+strings.Join(claims, "; "))
	}
	if p.TotalAdmitted > 0 || p.RefusedCitationRows > 0 {
		line := fmt.Sprintf("  citations: %d read from the message, %d shown", p.TotalAdmitted, p.CitationsCount)
		if p.RefusedCitationRows > 0 {
			line += fmt.Sprintf("; %d more could not be read", p.RefusedCitationRows)
		}
		lines = append(lines, line)
	}
	for _, c := range p.Citations {
		link := "no link"
		if c.Link != nil && *c.Link != "" {
			link = appText(*c.Link)
		}
		lines = append(lines, fmt.Sprintf("    [%d] id %s %q %s", c.Position, c.ID, appText(c.Title), link))
	}
	for _, l := range p.Losses {
		line := fmt.Sprintf("  native fields not read: %s x%d", l.Code, l.Count)
		if l.Meaning != "" {
			line += " (" + l.Meaning + ")"
		}
		lines = append(lines, line)
	}
	lines = append(lines, "  full record (the stored projection as a JSON string to decode again; not the raw message): "+p.FullInspection)
	return lines
}

// appCoverageLine is the short text form of a result's application evidence coverage: the
// non-zero states, how many returned messages name an app on the next line, then, when some
// evidence is not derived, the remedy on its own line.
func appCoverageLine(c *applicationCoverage) string {
	if c.State == "unknown" {
		return "app evidence: unknown (no Teams message returned)"
	}
	var parts []string
	for _, s := range appStates {
		if n := c.States[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, strings.ReplaceAll(s, "_", " ")))
		}
	}
	line := fmt.Sprintf("app evidence of %d returned Teams messages (mapper v%d): %s", c.ReturnedMessages, c.MapperVersion, strings.Join(parts, ", "))
	if c.Retained > 0 {
		line += fmt.Sprintf("; %d retained", c.Retained)
	}
	if c.unserved > 0 {
		line += fmt.Sprintf("\nposted by an app (names an app): at least %d of %d\n%d not derived or failed, so whether they name an app is unknown", c.PostedByApp, c.ReturnedMessages, c.unserved)
	} else {
		line += fmt.Sprintf("\nposted by an app (names an app): %d of %d", c.PostedByApp, c.ReturnedMessages)
	}
	if c.States[store.AppStateNotDerived] > 0 {
		line += "\nnot derived yet: " + appSyncRemedy
	}
	return line
}

// renderApplicationDetails prints, after a message table, what each returned message's application
// evidence claims; renderText prints the coverage line with the result's other summaries.
func (rt *runtime) renderApplicationDetails(r *listResult) {
	if r.ApplicationEvidenceCoverage == nil {
		return
	}
	var lines []string
	for _, item := range r.Items {
		switch x := item.(type) {
		case messageItem:
			lines = append(lines, appDetailLines("message "+x.ID+" (thread: "+threadTarget(x.ConversationID, x.ReplyChainID, x.ID)+")", x.App)...)
		case searchChatItem:
			lines = append(lines, appDetailLines("message "+x.ID+" (thread: "+threadTarget(x.ConversationID, x.ReplyChainID, x.ID)+")", x.App)...)
		}
	}
	if len(lines) > 0 {
		_, _ = fmt.Fprintln(rt.stdout)
	}
	for _, line := range lines {
		_, _ = fmt.Fprintln(rt.stdout, line)
	}
}

// applicationEvidenceHelp is the long help every command that returns archived Teams messages shares.
const applicationEvidenceHelp = "Application evidence: each returned Teams message carries app_observation, the app, reply and citation claims of its archived native JSON. Claims are under served: app_id, app_name, reply_to_id and citations (id as a decimal string, title, link, content_type). total_admitted is how many citations were read from the message and citations_count how many of them this item shows (at most 100; citations_truncated says when some are left out); refused_citation_rows counts the citation rows of the message that could not be read (not an object, or a field of the wrong type). A citation's link is the stored value, null when the message gave none: nothing derives it at read time, and the item's own link is the Teams link to the message, not a citation target. losses lists each kind of native field that could not be read, with its code, count and meaning; the codes are listed below, after the flags. These are the message's own claims, not a verified app identity or citation target, and one message stays one item.\n" +
	"full_inspection is an archive-only `m365crawl --db PATH --max-age 0 sql` command that prints the stored projection: app_id, app_name, reply_to_id, every citation that was read, losses and source_slots. It is not the raw message: rows that could not be read and nested citation content are not in it, and the raw message is messages.raw_json (see `m365crawl sql --help`). The command returns projection_json as a JSON string: decode it a second time. It selects by rowid, the number in observation_id; a row of message_app_observations is never updated or deleted, so the rowid always names the same projection. It names the same --db archive the read used (as an absolute path, double-quoted on Windows and single-quoted elsewhere when it needs quoting), so run it as printed.\n" +
	"app_observation.state is mapped (claims read), partial (some read; see losses), absent (no app or citation claims), failed (the native JSON could not be read; failure_code says why) or not_derived (not projected yet: run a writable `m365crawl sync`; --max-age 0 never derives it). mapped means the claims were read, not that an app or bot posted the message: a message names an app only when served.app_id or served.app_name is set. When the current representation failed, served is the last good one, with retained true and retained_reason current_failed. application_evidence_coverage counts the states of the Teams messages actually returned (after --limit), and its posted_by_app counts those whose served evidence names an app by app_id or app_name (the rows text output marks [app: NAME]); it is a lower bound, and the note says so, when a returned message is not_derived or failed with nothing served. When none is returned its state is unknown, not absent.\n"

// appSummaryHelp is the help of --fields app_summary, on the commands that accept the key.
const appSummaryHelp = "--fields app_summary gives each message a compact app_summary instead of the whole app_observation: state, app_name, app_id, reply_to_id and citations_total. citations_total is every citation read from the message (served.total_admitted), not the at most 100 an item shows; it and the three claims are null when nothing is served (not_derived, or failed with nothing retained). It is printed only when --fields asks for it.\n"

// appCodeCommands are the commands whose help ends with the application evidence codes.
var appCodeCommands = []string{"messages", "unread", "thread", "search", "calendar event"}

// writeAppCodesHelp prints every loss and failure code with its one-line meaning, after the rest
// of a command's help.
func writeAppCodesHelp(w io.Writer, cmd string) {
	if !contains(appCodeCommands, cmd) {
		return
	}
	var b strings.Builder
	b.WriteString("\nApplication evidence loss codes (served.losses[].code, native fields not read):\n")
	for _, code := range slices.Sorted(maps.Keys(botcitations.LossMeanings)) {
		b.WriteString(wrapWords("  ", code+": "+botcitations.LossMeanings[code]))
	}
	b.WriteString("\nApplication evidence failure codes (failure_code, when state is failed):\n")
	for _, code := range slices.Sorted(maps.Keys(appFailureMeanings)) {
		b.WriteString(wrapWords("  ", code+": "+appFailureMeanings[code]))
	}
	_, _ = io.WriteString(w, b.String())
}

func (messagesCmd) Help() string { return applicationEvidenceHelp + appSummaryHelp }
func (unreadCmd) Help() string   { return applicationEvidenceHelp + appSummaryHelp }
func (threadCmd) Help() string   { return applicationEvidenceHelp + appSummaryHelp }
func (calendarEventCmd) Help() string {
	return "The meeting chat's recent messages carry app_observation like `m365crawl messages`, and the event's application_evidence_coverage counts them.\n" + applicationEvidenceHelp
}

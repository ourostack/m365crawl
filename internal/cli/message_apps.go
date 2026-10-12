package cli

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ourostack/m365crawl/internal/botcitations"
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
	Losses              []botcitations.Loss       `json:"losses"`
	FullInspection      string                    `json:"full_inspection"`
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
	Retained         int            `json:"retained"`
	BackfillPending  bool           `json:"backfill_pending"`
	Note             string         `json:"note"`
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
		return "the current raw revision failed; served is the last good observation of this message, from mapper version " + strconv.Itoa(env.Served.Revision.MapperVersion)
	case env.FailureCode == store.AppProjectionCorrupt:
		return "the stored observation failed validation and is not shown; the message itself is unaffected"
	case env.State == store.AppStateFailed:
		return "the raw revision could not be read as application evidence; a sync does not change that, and the message itself is unaffected"
	case env.State == store.AppStatePartial:
		return "some native fields could not be read; losses counts them, and older revisions are not merged in"
	}
	return ""
}

func digestOrNil(d string) *string {
	if d == "" {
		return nil
	}
	return &d
}

// fullInspection is the archive-only command that prints an observation's complete stored JSON.
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
		TotalAdmitted: len(p.Citations), SourceSlots: p.SourceSlots, Losses: append([]botcitations.Loss{}, p.Losses...), FullInspection: fullInspection(rep.ID, db)}
	for i, c := range p.Citations {
		if i == appCitationPreview {
			out.CitationsTruncated = true
			break
		}
		out.Citations = append(out.Citations, applicationCitationItem{Position: c.Position, ID: c.ID, Title: c.Title, Link: c.Link, ContentType: c.ContentType, NestedContentPresent: c.NestedContentPresent})
	}
	out.CitationsCount = len(out.Citations)
	for _, l := range p.Losses {
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
		c.BackfillPending = c.BackfillPending || env.BackfillPending
	}
	switch {
	case len(envs) == 0:
		c.State, c.Note = "unknown", "no Teams message was returned, so application evidence is unknown here, not absent"
	default:
		c.Note = "counts cover only the returned Teams messages; app, reply and citation fields are each message's own native claims, not verified bot identity, Copilot history or resolved documents"
	}
	if c.States[store.AppStateNotDerived] > 0 {
		c.Note += "; not_derived messages have no evidence yet: " + appSyncRemedy
	}
	return c
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

// appMarker is the short tag a message's text column starts with when it carries served
// application evidence; empty otherwise.
func appMarker(it *applicationItem) string {
	if it == nil || it.Served == nil || it.Served.applicationPayloadItem == nil {
		return ""
	}
	tag := "app evidence"
	switch p := it.Served.applicationPayloadItem; {
	case p.AppName != nil && *p.AppName != "":
		tag = "app: " + appText(*p.AppName)
	case p.AppID != nil && *p.AppID != "":
		tag = "app id: " + appText(*p.AppID)
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
		lines = append(lines, fmt.Sprintf("  citations: %d of %d admitted shown; %d source rows refused", p.CitationsCount, p.TotalAdmitted, p.RefusedCitationRows))
	}
	for _, c := range p.Citations {
		link := "no link"
		if c.Link != nil && *c.Link != "" {
			link = appText(*c.Link)
		}
		lines = append(lines, fmt.Sprintf("    [%d] id %s %q %s", c.Position, c.ID, appText(c.Title), link))
	}
	for _, l := range p.Losses {
		lines = append(lines, fmt.Sprintf("  native fields not read: %s x%d", l.Code, l.Count))
	}
	lines = append(lines, "  full record: "+p.FullInspection)
	return lines
}

// appCoverageLine is the short text form of a result's application evidence coverage: the
// non-zero states, then, when some evidence is not derived, the remedy on its own line.
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
const applicationEvidenceHelp = "Application evidence: each returned Teams message carries app_observation, the app, reply and citation claims of its archived native JSON. Claims are under served: app_id, app_name, reply_to_id and citations (id as a decimal string, title, link, content_type), previewing at most 100 citations of total_admitted; losses counts native fields that could not be read, and full_inspection is an archive-only `m365crawl --db PATH --max-age 0 sql` command that prints the complete stored representation; it names the same --db archive the read used (as an absolute path, double-quoted on Windows and single-quoted elsewhere when it needs quoting), so run it as printed. These are the message's own claims, not a verified app identity or citation target, and one message stays one item.\n" +
	"app_observation.state is mapped (claims read), partial (some read; see losses), absent (no app or citation claims), failed (the native JSON could not be read; failure_code says why) or not_derived (not projected yet: run a writable `m365crawl sync`; --max-age 0 never derives it). When the current representation failed, served is the last good one, with retained true and retained_reason current_failed. application_evidence_coverage counts the states of the Teams messages actually returned (after --limit); when none is returned its state is unknown, not absent.\n"

func (messagesCmd) Help() string { return applicationEvidenceHelp }
func (unreadCmd) Help() string   { return applicationEvidenceHelp }
func (threadCmd) Help() string   { return applicationEvidenceHelp }
func (calendarEventCmd) Help() string {
	return "The meeting chat's recent messages carry app_observation like `m365crawl messages`, and the event's application_evidence_coverage counts them.\n" + applicationEvidenceHelp
}

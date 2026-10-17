package cli

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdrafts"
)

type draftsGroup struct {
	List draftsListCmd `cmd:"" help:"List strictly qualified own cached compose-body projections already archived; never refresh."`
	Show draftsShowCmd `cmd:"" help:"Inspect one cached body frame, optionally guarded by --body-revision; target evidence is not guarded."`
}

const draftCacheWarning = "Cached compose projection only: not_marked_removed is not latest-capture, editor-visible or server-unsent proof; decode failure may retain older bytes. Removed does not mean sent or discarded. Target evidence is freshly read and NOT body-guarded."

func (draftsGroup) Help() string {
	return "Inspect only the narrow archived Teams regular-draft format. " + draftCacheWarning + "\nNo acquisition, discovery, browser, send, edit, discard or shared-search inclusion. --max-age is syntax-checked but never refreshes; refresh_policy=explicit_ordinary_sync_only. Use ordinary sync explicitly and separately to refresh the archive; there is no drafts sync.\nList preview defaults to 300 Unicode code points; explicit --max-text 0 keeps full text. Show defaults full text/unexecuted HTML, never editor rendering. Native qualification: one Mac Space/team-side body; removed historical Chat shape only. Current chat/UI, native Windows, app-version range, lifecycle and all-drafts completeness are unqualified. draft_coverage survives fields/limits/zero results."
}

type draftsListCmd struct {
	IncludeRemoved bool `help:"Include last retained archive-removed bytes, not sent/discarded history."`
	Limit          int  `default:"50" help:"Positive item limit; scan completeness and exact totals are independent."`
}
type draftsShowCmd struct {
	ID             string `arg:"" help:"Full teams-draft:SHA256 locator printed by drafts list, not a Teams address."`
	BodyRevision   string `help:"Require this full SHA256 retained-wrapper body frame; targets and clocks outside that frame are NOT guarded."`
	IncludeRemoved bool   `help:"Opt in to last retained removed body bytes."`
}

func (draftsListCmd) Help() string { return (draftsGroup{}).Help() }
func (draftsShowCmd) Help() string { return (draftsGroup{}).Help() }

type draftExplicitFlags struct{ Acquisition, MaxText, Fields, BodyRevision bool }

func explicitDraftFlags(kctx *kong.Context) draftExplicitFlags {
	var f draftExplicitFlags
	for _, p := range kctx.Path {
		if p.Flag == nil || p.Resolved {
			continue
		}
		switch p.Flag.Name {
		case "teams-root", "outlook-root", "outlook-account", "outlook-profile":
			f.Acquisition = true
		case "max-text":
			f.MaxText = true
		case "fields":
			f.Fields = true
		case "body-revision":
			f.BodyRevision = true
		}
	}
	return f
}

type draftsResult struct {
	*listResult
	DraftCoverage store.TeamsDraftCoverage `json:"draft_coverage"`
	TotalExact    *int                     `json:"total_exact,omitempty"`
	archive       string
}
type draftItem struct {
	ID                    string                      `json:"id"`
	Source                string                      `json:"source"`
	Classification        string                      `json:"classification"`
	ArchiveSource         string                      `json:"archive_source"`
	TenantID              string                      `json:"tenant_id"`
	UserID                string                      `json:"user_id"`
	Database              string                      `json:"database"`
	Store                 string                      `json:"store"`
	WrapperConversationID string                      `json:"wrapper_conversation_id"`
	WrapperReplyChainID   string                      `json:"wrapper_reply_chain_id"`
	MessageMapKey         string                      `json:"message_map_key"`
	MessageID             string                      `json:"message_id"`
	ClientDraftID         teamsdrafts.Presence        `json:"client_draft_id"`
	BodyState             string                      `json:"body_state"`
	Text                  *string                     `json:"text"`
	HTML                  *string                     `json:"html,omitempty"`
	TextOrigin            string                      `json:"text_origin"`
	TextState             string                      `json:"text_state"`
	RenderFidelity        string                      `json:"render_fidelity"`
	BodyRevision          string                      `json:"body_revision"`
	BodyFrameComplete     bool                        `json:"body_frame_complete"`
	BodyGuardScope        string                      `json:"body_guard_scope"`
	TargetGuarded         bool                        `json:"target_guarded"`
	LastEdit              teamsdrafts.EditClock       `json:"last_edit"`
	VersioningTimestamp   teamsdrafts.Presence        `json:"versioning_timestamp"`
	FirstSeenAt           string                      `json:"first_seen_at"`
	RecordUpdatedAt       string                      `json:"record_updated_at"`
	RecordRemovedAt       *string                     `json:"record_removed_at"`
	ArchivePresence       string                      `json:"archive_presence"`
	TargetClaim           teamsdrafts.TargetClaim     `json:"target_claim"`
	Association           store.TeamsDraftAssociation `json:"association"`
	UnsentNow             string                      `json:"unsent_now"`
	UIState               string                      `json:"ui_state"`
	TextTruncated         bool                        `json:"text_truncated"`
	HTMLTruncated         bool                        `json:"html_truncated"`
	FullInspection        string                      `json:"full_inspection"`
}

var draftHashSyntax = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c *draftsListCmd) Run(rt *runtime, flags draftExplicitFlags) error {
	if flags.Acquisition {
		return errs.Usage("drafts reads reject explicit acquisition/link flags; read only the selected archive")
	}
	if err := checkCommandFields(rt, "drafts list", ""); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	out, err := rt.draftsRead(store.TeamsDraftFilter{Account: rt.account, IncludeRemoved: c.IncludeRemoved, Limit: c.Limit})
	if err != nil {
		return err
	}
	max := 300
	if flags.MaxText {
		max = rt.g.MaxText
	}
	out.Items = shape(rt, draftItems(out, rt, max, false, c.IncludeRemoved))
	return rt.write("drafts list", out)
}
func (c *draftsShowCmd) Run(rt *runtime, flags draftExplicitFlags) error {
	if flags.Acquisition || flags.Fields {
		return errs.Usage("drafts show rejects explicit acquisition/link flags and list-only --fields")
	}
	if !strings.HasPrefix(c.ID, "teams-draft:") || !draftHashSyntax.MatchString(strings.TrimPrefix(c.ID, "teams-draft:")) {
		return errs.Usage("drafts show needs a full teams-draft:SHA256 locator")
	}
	if (flags.BodyRevision || c.BodyRevision != "") && !draftHashSyntax.MatchString(c.BodyRevision) {
		return errs.Usage("--body-revision needs a full SHA256 digest")
	}
	out, err := rt.draftsRead(store.TeamsDraftFilter{Account: rt.account, ID: c.ID, BodyRevision: c.BodyRevision, IncludeRemoved: c.IncludeRemoved})
	if err != nil {
		return err
	}
	out.Items = shape(rt, draftItems(out, rt, rt.g.MaxText, true, c.IncludeRemoved))
	return rt.write("drafts show", out)
}
func (rt *runtime) draftsRead(f store.TeamsDraftFilter) (*draftsResult, error) {
	db := rt.archiveRef()
	if !filepath.IsAbs(db) {
		return nil, errs.Usage("cannot resolve the selected archive to an absolute path; supply an absolute --db")
	}
	st, err := store.OpenReadOnly(rt.ctx, db)
	if errors.Is(err, store.ErrNoArchive) {
		if f.ID != "" {
			return nil, draftError(store.ErrTeamsDraftUnknown)
		}
		zero := 0
		list := newList(nil, false)
		list.Note = draftCacheWarning
		return &draftsResult{list, store.TeamsDraftCoverage{ArchiveState: "archive_missing", ScopeState: "archive_missing", RefreshPolicy: "explicit_ordinary_sync_only", SelectedAccount: rt.account, ScanComplete: true, BodyScanComplete: true, AssociationScanComplete: true, AdvisoryState: "unavailable", Ordering: "tuple_order"}, &zero, db}, nil
	}
	if err != nil {
		return nil, asCoded(err)
	}
	snapshot, readErr := st.TeamsDrafts(rt.ctx, f)
	if err = errors.Join(readErr, st.Close()); err != nil {
		return nil, draftError(err)
	}
	list := newList(nil, snapshot.Truncated)
	list.Count = len(snapshot.Rows)
	list.Note = draftCacheWarning
	result := &draftsResult{list, snapshot.Coverage, snapshot.Total, db}
	// Hold typed rows only until the command applies its list/show text policy.
	result.Items = make([]any, len(snapshot.Rows))
	for i, row := range snapshot.Rows {
		result.Items[i] = row
	}
	return result, nil
}
func draftError(err error) error {
	code := ""
	switch {
	case errors.Is(err, store.ErrTeamsDraftUnknown):
		code = "teams_draft_unknown"
	case errors.Is(err, store.ErrTeamsDraftRemoved):
		code = "teams_draft_removed"
	case errors.Is(err, store.ErrTeamsDraftAmbiguous):
		code = "teams_draft_ambiguous"
	case errors.Is(err, store.ErrTeamsDraftBodyRevisionChanged):
		code = "teams_draft_body_revision_changed"
	case errors.Is(err, store.ErrTeamsDraftScanIncomplete):
		code = "teams_draft_scan_incomplete"
	}
	if code == "" {
		return asCoded(err)
	}
	return &errs.Coded{Code: code, Exit: errs.ExitUsage, Message: err.Error(), Fix: "Inspect drafts list in the same selected archive/account; incomplete scans do not establish absence. No acquisition was performed."}
}
func draftItems(result *draftsResult, rt *runtime, max int, show, includeRemoved bool) []draftItem {
	out := make([]draftItem, 0, len(result.Items))
	account := ""
	if rt.account != nil {
		account = rt.account.TenantID + "/" + rt.account.UserID
	}
	for _, raw := range result.Items {
		row := raw.(store.TeamsDraftRow)
		b := row.Body
		e := b.Envelope
		text, html := b.Text, b.HTML
		textTruncated, htmlTruncated := false, false
		if text != nil {
			s, trunc := truncateRunes(*text, max)
			text = &s
			textTruncated = trunc
		}
		if show && html != nil {
			s, trunc := truncateRunes(*html, max)
			html = &s
			htmlTruncated = trunc
		} else {
			html = nil
		}
		presence := "not_marked_removed"
		if e.RemovedAt != nil {
			presence = "removed"
		}
		out = append(out, draftItem{ID: b.ID, Source: "teams_draft", Classification: "cached_regular_draft_projection", ArchiveSource: e.Source, TenantID: e.TenantID, UserID: e.UserID, Database: e.Database, Store: e.Store, WrapperConversationID: b.WrapperConversationID, WrapperReplyChainID: b.WrapperReplyChainID, MessageMapKey: b.MemberKey, MessageID: b.MessageID, ClientDraftID: b.ClientDraftID, BodyState: b.BodyState, Text: text, HTML: html, TextOrigin: "message.content", TextState: "cached_projection", RenderFidelity: "not_editor_rendering", BodyRevision: b.BodyRevision, BodyFrameComplete: true, BodyGuardScope: "retained_wrapper_body_frame_only", TargetGuarded: false, LastEdit: b.LastEdit, VersioningTimestamp: b.VersioningTimestamp, FirstSeenAt: e.FirstSeenAt, RecordUpdatedAt: e.UpdatedAt, RecordRemovedAt: e.RemovedAt, ArchivePresence: presence, TargetClaim: b.Claim, Association: row.Association, UnsentNow: "unknown", UIState: "unverified", TextTruncated: textTruncated, HTMLTruncated: htmlTruncated, FullInspection: draftInspectionCommand(result.archive, account, b.ID, b.BodyRevision, includeRemoved)})
	}
	return out
}
func draftInspectionCommand(db, account, id, revision string, includeRemoved bool) string {
	words := []string{"m365crawl", "--db", quoteArg(db), "--max-age", "0", "--max-text", "0"}
	if account != "" {
		words = append(words, "--account", quoteArg(account))
	}
	words = append(words, "drafts", "show", quoteArg(id), "--body-revision", quoteArg(revision))
	if includeRemoved {
		words = append(words, "--include-removed")
	}
	return strings.Join(words, " ")
}

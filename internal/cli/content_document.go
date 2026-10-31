package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

const documentHelp = "`content fetch doc LINK` reads one OneDrive or SharePoint Office file through the optional WorkIQ source, as the archived Teams account WorkIQ is signed in as (--account selects one when the archive has several). LINK is a sharing link, a Doc.aspx link or the file's URL, such as the href of a file attached to a Teams message; it is used once and never stored.\n" +
	"Text enters the archive only when WorkIQ retrieve reports contentProtection \"allowed\" for that exact file, which Microsoft 365 Copilot reports only when the user holds the EXTRACT (Copy) usage right on encrypted content. If no encrypting label accounts for the file's protection, its bytes are inspected (up to 4 MB, never decrypted or kept): encryption without such a label is refused. Only the excerpts WorkIQ released are kept, so scope is always partial. Every fetch re-checks rights and a refusal deletes previously kept text. Refusals keep no text: document_extract_not_allowed, document_rights_unknown, document_protection_unattributed, document_protection_unverifiable, document_kind_unsupported. A live view-only (VIEW without EXTRACT) refusal has not been observed; any value other than \"allowed\", or none, refuses.\n" +
	"workiq-document:N is a document's archive ID for content show; content sources lists every fetched document and its latest check.\n"

type contentFetchDocCmd struct {
	Link string `arg:"" help:"One HTTPS SharePoint or OneDrive Office file link."`
}

func (contentFetchDocCmd) Help() string { return documentHelp }

// fetchWorkIQDocument is the explicit document fetch; a test seam.
var fetchWorkIQDocument = syncer.FetchWorkIQDocument

// documentFetchResult is one explicit document fetch's outcome.
type documentFetchResult struct {
	Status            string                    `json:"status"`
	ID                string                    `json:"id"`
	Code              string                    `json:"code,omitempty"`
	Account           string                    `json:"account"`
	Name              string                    `json:"name"`
	WebURL            string                    `json:"web_url"`
	DriveID           string                    `json:"drive_id"`
	ItemID            string                    `json:"item_id"`
	Scope             string                    `json:"scope"`
	Text              *string                   `json:"text"`
	TextTruncated     bool                      `json:"text_truncated"`
	Extracts          int                       `json:"extracts"`
	ServiceTruncated  bool                      `json:"service_truncated"`
	KeptTextDeleted   bool                      `json:"kept_text_deleted"`
	TextLogPending    bool                      `json:"text_log_pending"`
	Check             store.WorkIQDocumentCheck `json:"check"`
	Show              string                    `json:"show"`
	RefusalLiveStatus string                    `json:"refusal_live_status"`
}

// refusalLiveStatus says that only an admission has been observed against the live service.
const refusalLiveStatus = "unverified: no live view-only target has been observed; non-allowed and missing values refuse"

func (c *contentFetchDocCmd) Run(rt *runtime) error {
	if rt.g.OutlookAccount != "" || rt.g.OutlookProfile != "" {
		return errs.Usage("content fetch doc selects a WorkIQ Teams account with --account, not an Outlook profile")
	}
	report, err := fetchWorkIQDocument(rt.ctx, syncer.WorkIQDocumentOptions{DBPath: rt.dbPath, Account: rt.g.Account, Link: c.Link,
		Invocation: rt.workIQInvocation()})
	if err != nil {
		return asCoded(err)
	}
	res := documentFetchResult{Status: report.Check.Outcome, ID: fmt.Sprintf("workiq-document:%d", report.Write.DocumentID),
		Code: report.Check.Code, Account: report.Account, Name: report.Item.Name, WebURL: report.Item.WebURL,
		DriveID: report.Item.Ref.DriveID, ItemID: report.Item.Ref.ItemID, Scope: "none", Extracts: report.Extracts,
		ServiceTruncated: report.Truncated, KeptTextDeleted: report.Write.BodyDeleted, TextLogPending: report.Write.LogPending, Check: report.Check,
		RefusalLiveStatus: refusalLiveStatus}
	res.Show = rt.contentCommand("show", res.ID)
	if report.Check.Outcome == "admitted" {
		text, cut := truncateRunes(report.Text, rt.g.MaxText)
		res.Scope, res.Text, res.TextTruncated = "partial", &text, cut
	}
	if err := rt.write(rt.cmd, res); err != nil || res.Status != "refused" {
		return err
	}
	return documentRefusal(res.Code)
}

// documentRefusal is the coded result of a refused check. The check is archived; no text is.
func documentRefusal(code string) *errs.Coded {
	fix := map[string]string{
		syncer.DocumentExtractNotAllowed:      "Microsoft 365 reported that your rights on this file do not allow extracting its text (no EXTRACT/Copy usage right). Open the file yourself; m365crawl keeps no text.",
		syncer.DocumentRightsUnknown:          "WorkIQ returned no rights decision for this exact file (not indexed yet, or a reference-only result). Retry later with the same command; m365crawl keeps no text until rights are reported.",
		syncer.DocumentProtectionUnattributed: "The file is encrypted but no encrypting sensitivity label accounts for it, so the rights decision cannot be relied on. m365crawl keeps no text.",
		syncer.DocumentProtectionUnverifiable: "The file has no encrypting label and its bytes could not be inspected (larger than 4 MB or unavailable), so its protection is unknown. m365crawl keeps no text.",
		syncer.DocumentKindUnsupported:        "Only Word, PowerPoint and Excel Office Open XML files (.docx, .docm, .pptx, .pptm, .xlsx, .xlsm) are supported.",
	}[code]
	return &errs.Coded{Code: code, Exit: errs.ExitEnvironment, Message: "document text was not admitted (" + code + ")", Fix: fix}
}

func (r documentFetchResult) renderWorkIQ(rt *runtime) error {
	var out bytes.Buffer
	fmt.Fprintf(&out, "%s %s: %s", r.ID, oneLine(r.Name), r.Status)
	if r.Code != "" {
		fmt.Fprintf(&out, " (%s)", r.Code)
	}
	fmt.Fprintln(&out)
	writeDocumentCheck(&out, r.Check)
	if r.KeptTextDeleted {
		fmt.Fprintln(&out, "previously kept text deleted")
	}
	if r.Text != nil {
		fmt.Fprintf(&out, "scope partial: %d excerpt(s); service truncated %t\n\n%s\n", r.Extracts, r.ServiceTruncated, *r.Text)
	}
	fmt.Fprintf(&out, "refusal path: %s\n", r.RefusalLiveStatus)
	_, err := io.Copy(rt.stdout, &out)
	return err
}

func writeDocumentCheck(out *bytes.Buffer, c store.WorkIQDocumentCheck) {
	label := "none reported"
	if c.LabelID != nil {
		label = fmt.Sprintf("%s (%s); encrypting %s", oneLine(*c.LabelName), *c.LabelID, optionalBool(c.LabelEncrypted))
	}
	protection := "absent"
	if c.ContentProtection != nil {
		protection = *c.ContentProtection
	}
	fmt.Fprintf(out, "checked %s; contentProtection %s; label %s; bytes %s", c.CheckedAt.UTC().Format(time.RFC3339), protection, label, c.BytesState)
	if c.Attribution != "" {
		fmt.Fprintf(out, "; protection attributed to %s", c.Attribution)
	}
	fmt.Fprintln(out)
}

func optionalBool(v *bool) string {
	if v == nil {
		return "unknown"
	}
	return strconv.FormatBool(*v)
}

// workIQDocumentItem is a fetched document as content list, search and show return it.
type workIQDocumentItem struct {
	ID             string                      `json:"id"`
	Kind           string                      `json:"kind"`
	Account        string                      `json:"account"`
	Name           string                      `json:"name"`
	WebURL         string                      `json:"web_url"`
	DriveID        string                      `json:"drive_id"`
	ItemID         string                      `json:"item_id"`
	State          string                      `json:"state"`
	Scope          string                      `json:"scope"`
	FirstCheckedAt time.Time                   `json:"first_checked_at"`
	LastCheckedAt  time.Time                   `json:"last_checked_at"`
	LastCheck      store.WorkIQDocumentCheck   `json:"last_check"`
	Text           *string                     `json:"text"`
	TextTruncated  bool                        `json:"text_truncated"`
	Extracts       int                         `json:"extracts"`
	ServiceCut     bool                        `json:"service_truncated"`
	Checks         []store.WorkIQDocumentCheck `json:"checks,omitempty"`
	FullTextSQL    *string                     `json:"full_text_sql"`
}

func workIQDocumentItemOf(d store.WorkIQDocument, maxText int) workIQDocumentItem {
	it := workIQDocumentItem{ID: fmt.Sprintf("workiq-document:%d", d.ID), Kind: "workiq_document", Account: d.Account, Name: d.Name,
		WebURL: d.WebURL, DriveID: d.DriveID, ItemID: d.ItemID, State: d.Last.Outcome, Scope: "none",
		FirstCheckedAt: d.FirstCheckedAt, LastCheckedAt: d.LastCheckedAt, LastCheck: d.Last}
	if d.Body != nil {
		text, cut := truncateRunes(d.Body.Text, maxText)
		sql := fmt.Sprintf("select text from workiq_document_bodies where document_id=%d", d.ID)
		it.Scope, it.Text, it.TextTruncated, it.Extracts, it.ServiceCut, it.FullTextSQL = "partial", &text, cut, d.Body.Extracts, d.Body.Truncated, &sql
	}
	return it
}

func workIQDocumentID(value string) (int64, error) {
	raw, _ := strings.CutPrefix(value, "workiq-document:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, errs.Usage("expected workiq-document:N with a positive decimal int64")
	}
	return id, nil
}

func (rt *runtime) showWorkIQDocument(value string) error {
	id, err := workIQDocumentID(value)
	if err != nil {
		return err
	}
	return rt.contentRead("content show", func(st *store.Store) (*listResult, error) {
		if st == nil {
			return nil, store.ErrKnowledgeSourceUnknown
		}
		d, checks, err := st.WorkIQDocumentDetail(rt.ctx, id, 20)
		if errors.Is(err, store.ErrWorkIQDocumentUnknown) {
			return nil, store.ErrKnowledgeSourceUnknown
		}
		if err != nil {
			return nil, err
		}
		item := workIQDocumentItemOf(d, rt.g.MaxText)
		item.Checks = checks
		res := newList(shape(rt, []any{item}), false)
		res.WorkIQDocumentCoverage = workIQDocumentCoverageOf([]store.WorkIQDocument{d})
		return res, nil
	})
}

// workIQDocumentCoverage is every fetched document beside a content result, outside --fields.
type workIQDocumentCoverage struct {
	Documents         []workIQDocumentCoverageItem `json:"documents"`
	RefusalLiveStatus string                       `json:"refusal_live_status"`
}

type workIQDocumentCoverageItem struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	State         string    `json:"state"`
	Code          string    `json:"code,omitempty"`
	TextKept      bool      `json:"text_kept"`
	LastCheckedAt time.Time `json:"last_checked_at"`
}

func workIQDocumentCoverageOf(docs []store.WorkIQDocument) *workIQDocumentCoverage {
	if len(docs) == 0 {
		return nil
	}
	c := &workIQDocumentCoverage{Documents: []workIQDocumentCoverageItem{}, RefusalLiveStatus: refusalLiveStatus}
	for _, d := range docs {
		c.Documents = append(c.Documents, workIQDocumentCoverageItem{ID: fmt.Sprintf("workiq-document:%d", d.ID), Name: d.Name,
			State: d.Last.Outcome, Code: d.Last.Code, TextKept: d.Body != nil, LastCheckedAt: d.LastCheckedAt})
	}
	return c
}

// workIQDocumentHits are admitted documents for content list and search. Their native modified
// clock is unknown, so a bounded window or a knowledge --source-id excludes them.
func workIQDocumentHits(rt *runtime, st *store.Store, query string, filtered bool, limit int) ([]searchHit, []store.WorkIQDocument, error) {
	if st == nil {
		return nil, nil, nil
	}
	all, err := st.WorkIQDocuments(rt.ctx)
	if err != nil || filtered {
		return nil, all, err
	}
	matched := all
	if query != "" {
		if matched, err = st.SearchWorkIQDocuments(rt.ctx, query, limit+1); err != nil {
			return nil, all, err
		}
	}
	var hits []searchHit
	for _, d := range matched {
		if d.Body == nil {
			continue
		}
		hits = append(hits, searchHit{source: searchContent, cloudLabel: d.Name, cloudObservationID: d.ID, workIQDocument: true,
			item: workIQDocumentItemOf(d, rt.g.MaxText)})
	}
	return hits, all, nil
}

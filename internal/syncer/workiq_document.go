package syncer

import (
	"context"
	"path"
	"strings"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// Fixed refusal codes of an explicit document fetch. A refusal stores no text.
const (
	DocumentExtractNotAllowed      = "document_extract_not_allowed"
	DocumentRightsUnknown          = "document_rights_unknown"
	DocumentProtectionUnattributed = "document_protection_unattributed"
	DocumentProtectionUnverifiable = "document_protection_unverifiable"
	DocumentKindUnsupported        = "document_kind_unsupported"
)

// documentKinds are the Office Open XML files whose protection the byte check can attribute.
var documentKinds = map[string]bool{".docx": true, ".docm": true, ".pptx": true, ".pptm": true, ".xlsx": true, ".xlsm": true}

// WorkIQDocumentOptions selects one explicit document fetch.
type WorkIQDocumentOptions struct {
	DBPath     string
	Account    string // a WorkIQ chats-family account; empty selects the only one
	Link       string
	Invocation workiq.Invocation
}

// WorkIQDocumentReport is the outcome of one explicit document fetch.
type WorkIQDocumentReport struct {
	Account     string
	Item        workiq.DocumentItem
	Check       store.WorkIQDocumentCheck
	Text        string
	Extracts    int
	Truncated   bool
	Write       store.WorkIQDocumentWrite
	MatchedHits int
}

// documentJudgment is the rights gate's verdict on the retrieval hits for one file.
type documentJudgment struct {
	code       string // empty when the hits admit the file
	protection *string
	label      *workiq.DocumentLabel
	needBytes  bool // admitted only if the bytes prove no unattributed encryption
	text       string
	extracts   int
	truncated  bool
	matched    int
}

// judgeDocument admits a file only when every retrieval hit for that exact drive item says
// contentProtection "allowed" and the hits agree on the label. Any other value refuses; a missing
// value, or no hit for the item, is unknown rights and refuses too.
func judgeDocument(item workiq.DocumentItem, hits []workiq.RetrievalHit) documentJudgment {
	var j documentJudgment
	if !item.File || !documentKinds[strings.ToLower(path.Ext(item.Name))] {
		j.code = DocumentKindUnsupported
		return j
	}
	unknown, notAllowed, labelConflict, unlabeled := false, false, false, false
	seen := map[string]bool{}
	for _, hit := range hits {
		if hit.Ref != item.Ref {
			continue
		}
		j.matched++
		switch {
		case hit.ContentProtection == nil:
			unknown = true
		case *hit.ContentProtection != "allowed":
			notAllowed = true
			if j.protection == nil || *j.protection == "allowed" {
				j.protection = hit.ContentProtection
			}
		case j.protection == nil:
			j.protection = hit.ContentProtection
		}
		// A hit without a label does not agree with one that has a label.
		switch {
		case hit.Label == nil:
			unlabeled = true
		case j.label == nil:
			j.label = hit.Label
		case j.label.ID != hit.Label.ID || !sameBool(j.label.Encrypted, hit.Label.Encrypted):
			labelConflict = true
		}
		if hit.Snippet != "" && !seen[hit.Snippet] {
			seen[hit.Snippet] = true
			if j.text != "" {
				j.text += "\n\n"
			}
			j.text += hit.Snippet
			j.extracts++
		}
		j.truncated = j.truncated || hit.Truncated
	}
	switch {
	case notAllowed:
		j.code = DocumentExtractNotAllowed
	case j.matched == 0 || unknown || labelConflict || unlabeled && j.label != nil:
		j.code = DocumentRightsUnknown
	case j.label == nil || j.label.Encrypted == nil || !*j.label.Encrypted:
		j.needBytes = true
	}
	if j.code != "" {
		j.text, j.extracts, j.truncated = "", 0, false
	}
	return j
}

func sameBool(a, b *bool) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// judgeBytes finishes an admission that lacked an encrypting label: unencrypted bytes admit, and
// encryption the label does not account for, or bytes that cannot be read, refuse.
func judgeBytes(state string) string {
	switch state {
	case workiq.BytesNotEncrypted:
		return ""
	case workiq.BytesEncrypted:
		return DocumentProtectionUnattributed
	}
	return DocumentProtectionUnverifiable
}

// Test seams.
var (
	documentResolve  = (*workiq.Client).ResolveDocument
	documentRetrieve = (*workiq.Client).RetrieveDocument
	documentBlob     = (*workiq.Client).FetchDocumentBlob
)

// FetchWorkIQDocument resolves one link through the signed-in WorkIQ account bound to an archived
// Teams account, judges the user's rights on that exact file and appends the check. Text enters the
// archive only on admission, and only the excerpts WorkIQ retrieve released.
func FetchWorkIQDocument(ctx context.Context, options WorkIQDocumentOptions) (report WorkIQDocumentReport, err error) {
	var fact workiq.AccountFact
	if err := workIQRead(ctx, options.DBPath, func(s *store.Store) error {
		enabled, err := s.WorkIQEnabled(ctx)
		if err == nil && !enabled {
			err = workIQFailure("workiq_disabled")
		}
		var facts []workiq.AccountFact
		if err == nil {
			facts, err = s.WorkIQAccounts(ctx)
		}
		if err == nil {
			fact, err = documentAccount(facts, options.Account)
		}
		return err
	}); err != nil {
		return report, err
	}
	report.Account = fact.ID
	// A Teams account has no archived address to pin; its recorded user ID is checked instead.
	client, user, err := workIQOpenUser(ctx, options.Invocation, "")
	if err != nil {
		return report, err
	}
	check := store.WorkIQDocumentCheck{CheckedAt: workIQNow().UTC(), BytesState: "not_inspected", RightsSource: "workiq_retrieve",
		Qualification: store.WorkIQDocumentQualification}
	judgment, err := judgeWorkIQDocument(ctx, client, fact, user, options.Link, &report, &check)
	// The session ends before the archive is written, so a write never waits on WorkIQ.
	if closeErr := workIQClose(client); err == nil {
		err = closeErr
	}
	if err != nil {
		return report, err
	}
	check.ContentProtection = judgment.protection
	if judgment.label != nil {
		check.LabelID, check.LabelName, check.LabelEncrypted = &judgment.label.ID, &judgment.label.Name, judgment.label.Encrypted
	}
	check.Outcome, check.Code = "admitted", judgment.code
	switch {
	case judgment.code != "":
		check.Outcome = "refused"
	case check.BytesState == "not_inspected":
		check.Attribution = "label"
	default:
		check.Attribution = "none"
	}
	item := report.Item
	record := store.WorkIQDocumentRecord{Account: fact.ID, DriveID: item.Ref.DriveID, ItemID: item.Ref.ItemID, Name: item.Name, WebURL: item.WebURL,
		Check: check, Text: judgment.text, Extracts: judgment.extracts, Truncated: judgment.truncated}
	err = workIQWrite(ctx, options.DBPath, func(ctx context.Context, s *store.Store) error {
		var err error
		report.Write, err = s.RecordWorkIQDocument(ctx, record)
		return err
	})
	if err != nil {
		return report, err
	}
	check.ID = report.Write.CheckID
	report.Check, report.Text, report.Extracts, report.Truncated = check, judgment.text, judgment.extracts, judgment.truncated
	return report, nil
}

// judgeWorkIQDocument does the WorkIQ calls of one fetch: identity, resolution, rights and, when
// no encrypting label accounts for the protection, the byte check.
func judgeWorkIQDocument(ctx context.Context, client *workiq.Client, fact workiq.AccountFact, user workiq.User, link string,
	report *WorkIQDocumentReport, check *store.WorkIQDocumentCheck) (documentJudgment, error) {
	if fact.UserID == "" || user.ID != fact.UserID {
		return documentJudgment{}, workIQFailure("workiq_identity_mismatch")
	}
	item, _, err := documentResolve(client, ctx, link)
	if err != nil {
		return documentJudgment{}, err
	}
	report.Item = item
	judgment := judgeDocument(item, nil)
	if judgment.code != DocumentKindUnsupported {
		query := strings.TrimSuffix(item.Name, path.Ext(item.Name))
		hits, err := documentRetrieve(client, ctx, item, query)
		if err != nil {
			return judgment, err
		}
		judgment = judgeDocument(item, hits)
	}
	report.MatchedHits = judgment.matched
	if judgment.needBytes {
		if item.Size > workiq.MaxDocumentBlob {
			check.BytesState, judgment.code = "unavailable", DocumentProtectionUnverifiable
		} else {
			data, status, err := documentBlob(client, ctx, item.Ref)
			switch {
			case err != nil && status == 0:
				return judgment, err
			case err != nil:
				check.BytesState, judgment.code = "unavailable", DocumentProtectionUnverifiable
			default:
				check.BytesState = workiq.InspectOffice(data)
				judgment.code = judgeBytes(check.BytesState)
			}
		}
		if judgment.code != "" {
			judgment.text, judgment.extracts, judgment.truncated = "", 0, false
		}
	}
	return judgment, nil
}

// documentAccount selects the WorkIQ account a document fetch runs as: an archived Teams (chats)
// account with a recorded user, named by --account or the only one there is.
func documentAccount(facts []workiq.AccountFact, selector string) (workiq.AccountFact, error) {
	var chats []workiq.AccountFact
	for _, fact := range facts {
		if fact.Family == "chats" {
			chats = append(chats, fact)
		}
	}
	if selector != "" {
		for _, fact := range chats {
			if fact.ID == selector {
				return fact, nil
			}
		}
		return workiq.AccountFact{}, workIQFailure("workiq_chat_account_unmatched")
	}
	switch len(chats) {
	case 0:
		return workiq.AccountFact{}, workIQFailure("workiq_chat_account_unmatched")
	case 1:
		return chats[0], nil
	}
	return workiq.AccountFact{}, workIQFailure("workiq_account_required")
}

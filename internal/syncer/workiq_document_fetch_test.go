package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

const syntheticDocumentLink = "https://contoso.sharepoint.com/:w:/t/team/EabcSyntheticShare"

func documentArchive(t *testing.T, enabled bool, accounts ...teamsdesktop.Account) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "archive.db")
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for _, a := range accounts {
		if err := st.ApplyAccount(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetWorkIQEnabled(context.Background(), enabled); err != nil {
		t.Fatal(err)
	}
	return db
}

var syntheticTeams = teamsdesktop.Account{TenantID: "synthetic-tenant", UserID: "synthetic-user"}

func fetchSyntheticDocument(t *testing.T, db, docCase string) (WorkIQDocumentReport, error) {
	t.Helper()
	t.Setenv("WORKIQ_DOCUMENT_CASE", docCase)
	return FetchWorkIQDocument(context.Background(), WorkIQDocumentOptions{DBPath: db, Link: syntheticDocumentLink,
		Invocation: workiq.Invocation{ExplicitTeamsRoot: true}})
}

func TestFetchWorkIQDocumentOutcomes(t *testing.T) {
	workIQAcquisitionStub(t)
	db := documentArchive(t, true, syntheticTeams)
	for _, tc := range []struct {
		docCase, outcome, code, bytes, attribution string
	}{
		{"labeled", "admitted", "", "not_inspected", "label"},
		{"unlabeled-plain", "admitted", "", "not_encrypted", "none"},
		{"unlabeled-encrypted", "refused", DocumentProtectionUnattributed, "encrypted", ""},
		{"unlabeled-blob-missing", "refused", DocumentProtectionUnverifiable, "unavailable", ""},
		{"large-unlabeled", "refused", DocumentProtectionUnverifiable, "unavailable", ""},
		{"refused", "refused", DocumentExtractNotAllowed, "not_inspected", ""},
		{"folder", "refused", DocumentKindUnsupported, "not_inspected", ""},
	} {
		report, err := fetchSyntheticDocument(t, db, tc.docCase)
		if err != nil || report.Check.Outcome != tc.outcome || report.Check.Code != tc.code || report.Check.BytesState != tc.bytes ||
			report.Check.Attribution != tc.attribution || (tc.outcome == "admitted") != (report.Text != "") || report.Account != "synthetic-tenant/synthetic-user" {
			t.Fatalf("%s: %+v %v", tc.docCase, report, err)
		}
	}
}

func TestFetchWorkIQDocumentFailures(t *testing.T) {
	workIQAcquisitionStub(t)
	ctx := context.Background()
	code := func(err error) string {
		var coded *errs.Coded
		if errors.As(err, &coded) {
			return coded.Code
		}
		return ""
	}
	db := documentArchive(t, true, syntheticTeams)
	for docCase, want := range map[string]string{"missing": "workiq_failed", "tool-error": "workiq_failed", "unlabeled-blob-error": "workiq_failed"} {
		if _, err := fetchSyntheticDocument(t, db, docCase); code(err) != want {
			t.Fatalf("%s: %v", docCase, err)
		}
	}
	if _, err := fetchSyntheticDocument(t, documentArchive(t, false, syntheticTeams), "labeled"); code(err) != "workiq_disabled" {
		t.Fatal(err)
	}
	if _, err := fetchSyntheticDocument(t, documentArchive(t, true), "labeled"); code(err) != "workiq_chat_account_unmatched" {
		t.Fatal(err)
	}
	t.Setenv("WORKIQ_ACQUISITION_ME", "unbound-other")
	if _, err := fetchSyntheticDocument(t, db, "labeled"); code(err) != "workiq_identity_mismatch" {
		t.Fatal(err)
	}
	t.Setenv("WORKIQ_ACQUISITION_ME", "")
	if _, err := FetchWorkIQDocument(ctx, WorkIQDocumentOptions{DBPath: filepath.Join(t.TempDir(), "missing", "x.db"), Link: syntheticDocumentLink}); err == nil {
		t.Fatal("a missing archive was read")
	}

	boom := errors.New("synthetic failure")
	restore := func(set func()) { t.Helper(); set() }
	oldOpen, oldClose, oldWrite := workIQOpen, workIQClose, workIQWrite
	defer restore(func() { workIQOpen, workIQClose, workIQWrite = oldOpen, oldClose, oldWrite })

	workIQOpen = func(context.Context, workiq.Invocation, string) (*workiq.Client, error) { return nil, boom }
	if _, err := fetchSyntheticDocument(t, db, "labeled"); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	workIQOpen = oldOpen
	workIQClose = func(c *workiq.Client) error { _ = oldClose(c); return boom }
	if _, err := fetchSyntheticDocument(t, db, "labeled"); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	workIQClose = oldClose
	workIQWrite = func(context.Context, string, func(context.Context, *store.Store) error) error { return boom }
	if _, err := fetchSyntheticDocument(t, db, "labeled"); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

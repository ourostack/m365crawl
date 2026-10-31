package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const documentLink = "https://contoso.sharepoint.com/:w:/t/team/EabcSyntheticShare?e=1"

func documentEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	e.emptyArchive()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyAccount(context.Background(), teamsdesktop.Account{TenantID: "synthetic-tenant", UserID: "synthetic-user"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	workIQCLITransport(t, e.db)
	t.Setenv("HOME", t.TempDir())
	transcript := filepath.Join(t.TempDir(), "documents.jsonl")
	t.Setenv("WORKIQ_DOCUMENT_TRANSCRIPT", transcript)
	if code, _, stderr := e.run("--json", "workiq", "enable"); code != 0 {
		t.Fatal(stderr)
	}
	return e, transcript
}

func documentCalls(t *testing.T, transcript string) []string {
	t.Helper()
	raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic transcript.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var tools []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var call struct{ Tool string }
		if json.Unmarshal([]byte(line), &call) != nil {
			t.Fatal(line)
		}
		tools = append(tools, call.Tool)
	}
	_ = os.Remove(transcript)
	return tools
}

func errorCode(t *testing.T, stderr string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		var doc struct{ Error struct{ Code string } }
		if json.Unmarshal([]byte(line), &doc) == nil && doc.Error.Code != "" {
			return doc.Error.Code
		}
	}
	return ""
}

func TestContentFetchDocAdmitsOnlyAllowedTextAndRefusalDeletesIt(t *testing.T) {
	e, transcript := documentEnv(t)
	t.Setenv("WORKIQ_DOCUMENT_CASE", "labeled")
	code, out, stderr := e.run("--json", "content", "fetch", "doc", documentLink)
	if code != 0 {
		t.Fatal(code, stderr)
	}
	doc := decode(t, out)
	check := doc["check"].(map[string]any)
	if doc["status"] != "admitted" || doc["scope"] != "partial" || !strings.Contains(doc["text"].(string), "purple walrus") ||
		doc["account"] != "synthetic-tenant/synthetic-user" || check["content_protection"] != "allowed" ||
		check["label_id"] != "9fbde396-1a24-4c79-8edf-9254a0f35055" || check["label_encrypted"] != true ||
		check["protection_attribution"] != "label" || check["bytes_state"] != "not_inspected" || check["rights_source"] != "workiq_retrieve" ||
		!strings.HasPrefix(doc["refusal_live_status"].(string), "unverified") {
		t.Fatalf("admission: %s", out)
	}
	if strings.Contains(out, "EabcSyntheticShare") {
		t.Fatal("the sharing link was echoed")
	}
	if calls := documentCalls(t, transcript); strings.Join(calls, ",") != "fetch,retrieve" {
		t.Fatalf("an encrypting label still fetched bytes: %v", calls)
	}
	id := doc["id"].(string)

	_, out, _ = e.run("--json", "content", "search", "walrus")
	if !strings.Contains(out, `"kind":"workiq_document"`) || !strings.Contains(out, "purple walrus") || !strings.Contains(out, `"workiq_document_coverage"`) {
		t.Fatalf("search: %s", out)
	}
	_, out, _ = e.run("--json", "content", "show", id)
	if !strings.Contains(out, "purple walrus") || !strings.Contains(out, `"checks":[`) {
		t.Fatalf("show: %s", out)
	}
	_, out, _ = e.run("--json", "content", "sources")
	if !strings.Contains(out, `"text_kept":true`) {
		t.Fatalf("sources: %s", out)
	}

	t.Setenv("WORKIQ_DOCUMENT_CASE", "refused")
	code, out, stderr = e.run("--json", "content", "fetch", "doc", documentLink)
	if code != 3 || errorCode(t, stderr) != "document_extract_not_allowed" {
		t.Fatal("refusal exit", code, stderr)
	}
	doc = decode(t, out)
	if doc["status"] != "refused" || doc["text"] != nil || doc["kept_text_deleted"] != true || doc["id"] != id ||
		doc["check"].(map[string]any)["content_protection"] != "blocked" {
		t.Fatalf("refusal: %s", out)
	}
	_, out, _ = e.run("--json", "content", "search", "walrus")
	if strings.Contains(out, "purple walrus") || strings.Contains(out, `"kind":"workiq_document"`) {
		t.Fatalf("refused text still readable: %s", out)
	}
	_, out, _ = e.run("--json", "content", "show", id)
	if strings.Contains(out, "purple walrus") || !strings.Contains(out, `"state":"refused"`) || strings.Count(out, `"check_id"`) < 3 {
		t.Fatalf("show after refusal: %s", out)
	}
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, rows, _, err := st.SQL(context.Background(), `select (select count(*) from workiq_document_bodies),(select count(*) from workiq_document_fts where workiq_document_fts match 'walrus')`, 5)
	if err != nil || rows[0][0] != int64(0) || rows[0][1] != int64(0) {
		t.Fatal("refused text left in the archive", rows, err)
	}
}

func TestContentFetchDocRefusalCodes(t *testing.T) {
	for _, tc := range []struct {
		docCase, code, calls, bytes string
	}{
		{"reference", "document_rights_unknown", "fetch,retrieve", "not_inspected"},
		{"no-hit", "document_rights_unknown", "fetch,retrieve", "not_inspected"},
		{"other-item", "document_rights_unknown", "fetch,retrieve", "not_inspected"},
		{"unlabeled-encrypted", "document_protection_unattributed", "fetch,retrieve,fetch_blob", "encrypted"},
		{"unlabeled-blob-missing", "document_protection_unverifiable", "fetch,retrieve,fetch_blob", "unavailable"},
		{"large-unlabeled", "document_protection_unverifiable", "fetch,retrieve", "unavailable"},
		{"folder", "document_kind_unsupported", "fetch", "not_inspected"},
	} {
		t.Run(tc.docCase, func(t *testing.T) {
			e, transcript := documentEnv(t)
			t.Setenv("WORKIQ_DOCUMENT_CASE", tc.docCase)
			code, out, stderr := e.run("--json", "content", "fetch", "doc", documentLink)
			if code != 3 || errorCode(t, stderr) != tc.code {
				t.Fatal(code, stderr)
			}
			doc := decode(t, out)
			if doc["status"] != "refused" || doc["text"] != nil || doc["code"] != tc.code || doc["check"].(map[string]any)["bytes_state"] != tc.bytes {
				t.Fatalf("%s", out)
			}
			if calls := documentCalls(t, transcript); strings.Join(calls, ",") != tc.calls {
				t.Fatalf("calls %v", calls)
			}
			if strings.Contains(out, "walrus") {
				t.Fatal("refusal printed text")
			}
		})
	}
}

func TestContentFetchDocAdmitsUnlabeledPlainBytes(t *testing.T) {
	e, transcript := documentEnv(t)
	t.Setenv("WORKIQ_DOCUMENT_CASE", "unlabeled-plain")
	code, out, stderr := e.run("--json", "content", "fetch", "doc", documentLink)
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, out)
	check := doc["check"].(map[string]any)
	if doc["status"] != "admitted" || check["protection_attribution"] != "none" || check["bytes_state"] != "not_encrypted" || check["label_id"] != nil {
		t.Fatalf("%s", out)
	}
	if calls := documentCalls(t, transcript); strings.Join(calls, ",") != "fetch,retrieve,fetch_blob" {
		t.Fatalf("calls %v", calls)
	}
}

func TestContentFetchDocFailuresRecordNothing(t *testing.T) {
	e, transcript := documentEnv(t)
	for _, tc := range []struct {
		docCase string
		args    []string
		exit    int
		code    string
	}{
		{"tool-error", []string{"content", "fetch", "doc", documentLink}, 1, "workiq_failed"},
		{"missing", []string{"content", "fetch", "doc", documentLink}, 1, ""},
		{"labeled", []string{"content", "fetch", "doc", "https://evil.example/x.docx"}, 2, "document_link_unsupported"},
		{"labeled", []string{"--account", "synthetic-tenant/other", "content", "fetch", "doc", documentLink}, 1, "workiq_chat_account_unmatched"},
		{"labeled", []string{"--outlook-profile", "Main", "content", "fetch", "doc", documentLink}, 2, ""},
	} {
		t.Setenv("WORKIQ_DOCUMENT_CASE", tc.docCase)
		code, out, stderr := e.run(append([]string{"--json"}, tc.args...)...)
		if code != tc.exit || out != "" || tc.code != "" && errorCode(t, stderr) != tc.code {
			t.Fatal(tc.args, tc.docCase, code, out, stderr)
		}
	}
	if calls := documentCalls(t, transcript); strings.Join(calls, ",") != "fetch,retrieve,fetch" {
		t.Fatalf("calls %v", calls)
	}
	if code, _, _ := e.run("--json", "workiq", "disable"); code != 0 {
		t.Fatal("disable")
	}
	t.Setenv("WORKIQ_DOCUMENT_CASE", "labeled")
	if code, _, stderr := e.run("--json", "content", "fetch", "doc", documentLink); errorCode(t, stderr) != "workiq_disabled" || code == 0 {
		t.Fatal(code, stderr)
	}
	if calls := documentCalls(t, transcript); len(calls) != 0 {
		t.Fatalf("a disabled source started WorkIQ: %v", calls)
	}
	_, out, _ := e.run("--json", "content", "sources")
	if strings.Contains(out, "workiq_document") {
		t.Fatalf("a failure recorded a document: %s", out)
	}
}

func TestContentFetchDocTextAndReadEdges(t *testing.T) {
	e, _ := documentEnv(t)
	t.Setenv("WORKIQ_DOCUMENT_CASE", "labeled")
	code, out, stderr := e.run("--format", "text", "content", "fetch", "doc", documentLink)
	if code != 0 || !strings.Contains(out, "workiq-document:1 Synthetic plan.docx: admitted") || !strings.Contains(out, "contentProtection allowed") ||
		!strings.Contains(out, "encrypting true") || !strings.Contains(out, "purple walrus") || !strings.Contains(out, "refusal path: unverified") {
		t.Fatal(code, out, stderr)
	}
	t.Setenv("WORKIQ_DOCUMENT_CASE", "refused")
	if code, out, _ := e.run("--format", "text", "content", "fetch", "doc", documentLink); code != 3 || !strings.Contains(out, "refused (document_extract_not_allowed)") ||
		!strings.Contains(out, "previously kept text deleted") || strings.Contains(out, "walrus") {
		t.Fatal(code, out)
	}
	_, out, _ = e.run("--json", "content", "list")
	if strings.Contains(out, `"kind":"workiq_document"`) || !strings.Contains(out, `"text_kept":false`) {
		t.Fatalf("a refused document was listed: %s", out)
	}
	t.Setenv("WORKIQ_DOCUMENT_CASE", "labeled")
	if code, out, _ := e.run("--json", "content", "fetch", "doc", documentLink); code != 0 || !strings.Contains(out, `"check_id":3`) {
		t.Fatal(code, out)
	}
	_, out, _ = e.run("--json", "content", "list", "--modified-since", "2020-01-01")
	if strings.Contains(out, `"kind":"workiq_document"`) {
		t.Fatalf("a document without a modified clock matched a window: %s", out)
	}
	_, out, _ = e.run("--json", "content", "list")
	if !strings.Contains(out, `"kind":"workiq_document"`) {
		t.Fatalf("an admitted document was not listed: %s", out)
	}
	for _, id := range []string{"workiq-document:x", "workiq-document:0", "workiq-document:01"} {
		if code, _, _ := e.run("--json", "content", "show", id); code != 2 {
			t.Fatal(id, code)
		}
	}
	if code, _, stderr := e.run("--json", "content", "show", "workiq-document:99"); code == 0 || errorCode(t, stderr) != "content_unknown" {
		t.Fatal(code, stderr)
	}
	e.exec(`drop table workiq_document_fts; create table workiq_document_fts(title,text)`)
	if code, _, _ := e.run("--json", "content", "search", "walrus"); code == 0 {
		t.Fatal("a broken document index was hidden")
	}
	e.exec(`drop trigger workiq_document_bodies_fts_insert; drop trigger workiq_document_bodies_fts_delete; drop table workiq_document_bodies; create table workiq_document_bodies(x)`)
	for _, args := range [][]string{{"content", "list"}, {"content", "sources"}, {"content", "show", "workiq-document:1"}} {
		if code, _, _ := e.run(append([]string{"--json"}, args...)...); code == 0 {
			t.Fatal("damaged document state was hidden", args)
		}
	}
}

func TestContentReadsWithoutArchiveIgnoreDocuments(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.run("--json", "content", "show", "workiq-document:1"); code == 0 || errorCode(t, stderr) != "content_unknown" {
		t.Fatal(code, stderr)
	}
	if code, out, _ := e.run("--json", "content", "list"); code != 0 || strings.Contains(out, "workiq_document") {
		t.Fatal(code, out)
	}
}

func TestWriteDocumentCheckVariants(t *testing.T) {
	var out bytes.Buffer
	writeDocumentCheck(&out, store.WorkIQDocumentCheck{BytesState: "not_inspected"})
	id, name := "9fbde396-1a24-4c79-8edf-9254a0f35055", "Label"
	writeDocumentCheck(&out, store.WorkIQDocumentCheck{LabelID: &id, LabelName: &name, BytesState: "not_inspected"})
	no := false
	writeDocumentCheck(&out, store.WorkIQDocumentCheck{LabelID: &id, LabelName: &name, LabelEncrypted: &no, BytesState: "not_encrypted", Attribution: "none"})
	if text := out.String(); !strings.Contains(text, "contentProtection absent; label none reported") || !strings.Contains(text, "encrypting unknown") ||
		!strings.Contains(text, "encrypting false; bytes not_encrypted; protection attributed to none") {
		t.Fatal(text)
	}
}

func TestSortPutsDocumentsAfterOtherUnknownClockContent(t *testing.T) {
	for _, other := range []searchHit{{source: searchContent, cloudLabel: "z"}, {source: searchContent, engage: true, cloudLabel: "z"}} {
		hits := []searchHit{{source: searchContent, workIQDocument: true, cloudLabel: "a"}, other}
		sortSearchHits(hits)
		if hits[0].workIQDocument || !hits[1].workIQDocument {
			t.Fatalf("document sorted before other unknown-clock content %+v", other)
		}
	}
}

func TestSharedSearchIncludesAdmittedDocuments(t *testing.T) {
	e, _ := documentEnv(t)
	t.Setenv("WORKIQ_DOCUMENT_CASE", "labeled")
	if code, _, stderr := e.run("--json", "content", "fetch", "doc", documentLink); code != 0 {
		t.Fatal(stderr)
	}
	for _, args := range [][]string{{"search", "walrus", "--source", "content"}, {"search", "walrus"}} {
		code, out, stderr := e.run(append([]string{"--json", "--max-age", "0"}, args...)...)
		if code != 0 || !strings.Contains(out, `"kind":"workiq_document"`) || !strings.Contains(out, `"source":"content"`) ||
			!strings.Contains(out, "purple walrus") || !strings.Contains(out, `"workiq_document_coverage"`) {
			t.Fatalf("%v: %d %s %s", args, code, out, stderr)
		}
	}
	for _, args := range [][]string{{"search", "walrus", "--source", "content"}, {"content", "list"}, {"content", "show", "workiq-document:1"}} {
		code, out, _ := e.run(append([]string{"--format", "text", "--max-age", "0"}, args...)...)
		if code != 0 || !strings.Contains(out, "Synthetic plan.docx") || !strings.Contains(out, "Synthetic dummy t") ||
			!strings.Contains(out, "workiq-document:1 Synthetic plan.docx: admitted; text kept true") {
			t.Fatalf("text %v: %d %s", args, code, out)
		}
	}
	_, out, _ := e.run("--json", "--max-age", "0", "search", "walrus", "--source", "content", "--since", "2020-01-01")
	if strings.Contains(out, `"kind":"workiq_document"`) {
		t.Fatalf("a document without a modified clock matched a window: %s", out)
	}
	t.Setenv("WORKIQ_DOCUMENT_CASE", "refused")
	if code, _, _ := e.run("--json", "content", "fetch", "doc", documentLink); code != 3 {
		t.Fatal(code)
	}
	_, out, _ = e.run("--json", "--max-age", "0", "search", "walrus", "--source", "content")
	if strings.Contains(out, "purple walrus") || strings.Contains(out, `"kind":"workiq_document"`) || !strings.Contains(out, `"text_kept":false`) {
		t.Fatalf("refused text was searchable: %s", out)
	}
	e.exec(`drop table workiq_document_fts; create table workiq_document_fts(title,text)`)
	if code, _, _ := e.run("--json", "--max-age", "0", "search", "walrus", "--source", "content"); code == 0 {
		t.Fatal("a broken document index was hidden from shared search")
	}
}

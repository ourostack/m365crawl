package syncer

import (
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func docItem() workiq.DocumentItem {
	return workiq.DocumentItem{Ref: workiq.DocumentRef{DriveID: "b!drive0001", ItemID: "ITEM0001AB"}, Name: "Plan.docx",
		WebURL: "https://contoso.sharepoint.com/sites/s/Plan.docx", File: true}
}

func docHit(protection *string, label *workiq.DocumentLabel, snippet string) workiq.RetrievalHit {
	return workiq.RetrievalHit{Ref: docItem().Ref, ContentProtection: protection, Label: label, Snippet: snippet}
}

func TestJudgeDocument(t *testing.T) {
	encrypted := &workiq.DocumentLabel{ID: "9fbde396-1a24-4c79-8edf-9254a0f35055", Name: "Confidential", Encrypted: ptr(true)}
	plain := &workiq.DocumentLabel{ID: "f42aa342-8706-4288-bd11-ebb85995028c", Name: "General", Encrypted: ptr(false)}
	other := docHit(ptr("allowed"), encrypted, "other file text")
	other.Ref.ItemID = "OTHERITEM01"
	for _, tc := range []struct {
		name      string
		item      func(*workiq.DocumentItem)
		hits      []workiq.RetrievalHit
		code      string
		needBytes bool
		text      string
	}{
		{name: "allowed with encrypting label", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), encrypted, "a")}, text: "a"},
		{name: "duplicate snippets merge", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), encrypted, "a"), docHit(ptr("allowed"), encrypted, "a"), docHit(ptr("allowed"), encrypted, "b")}, text: "a\n\nb"},
		{name: "any other value refuses", hits: []workiq.RetrievalHit{docHit(ptr("blocked"), encrypted, "a")}, code: DocumentExtractNotAllowed},
		{name: "other value beats allowed", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), encrypted, "a"), docHit(ptr("other"), encrypted, "")}, code: DocumentExtractNotAllowed},
		{name: "reference-only hit is unknown", hits: []workiq.RetrievalHit{docHit(nil, nil, "")}, code: DocumentRightsUnknown},
		{name: "a missing value among allowed is unknown", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), encrypted, "a"), docHit(nil, nil, "")}, code: DocumentRightsUnknown},
		{name: "no hit is unknown", code: DocumentRightsUnknown},
		{name: "another item's hit is ignored", hits: []workiq.RetrievalHit{other}, code: DocumentRightsUnknown},
		{name: "conflicting labels are unknown", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), encrypted, "a"), docHit(ptr("allowed"), plain, "b")}, code: DocumentRightsUnknown},
		{name: "labeled and unlabeled hits disagree", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), encrypted, "a"), docHit(ptr("allowed"), nil, "b")}, code: DocumentRightsUnknown},
		{name: "unlabeled then labeled hits disagree", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), nil, "a"), docHit(ptr("allowed"), encrypted, "b")}, code: DocumentRightsUnknown},
		{name: "no label needs bytes", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), nil, "a")}, needBytes: true, text: "a"},
		{name: "non-encrypting label needs bytes", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), plain, "a")}, needBytes: true, text: "a"},
		{name: "label without encryption flag needs bytes", hits: []workiq.RetrievalHit{docHit(ptr("allowed"), &workiq.DocumentLabel{ID: encrypted.ID}, "a")}, needBytes: true, text: "a"},
		{name: "folder", item: func(i *workiq.DocumentItem) { i.File = false }, hits: []workiq.RetrievalHit{docHit(ptr("allowed"), encrypted, "a")}, code: DocumentKindUnsupported},
		{name: "pdf", item: func(i *workiq.DocumentItem) { i.Name = "Plan.pdf" }, hits: []workiq.RetrievalHit{docHit(ptr("allowed"), encrypted, "a")}, code: DocumentKindUnsupported},
		{name: "upper-case extension", item: func(i *workiq.DocumentItem) { i.Name = "PLAN.PPTX" }, hits: []workiq.RetrievalHit{docHit(ptr("allowed"), encrypted, "a")}, text: "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := docItem()
			if tc.item != nil {
				tc.item(&item)
			}
			j := judgeDocument(item, tc.hits)
			if j.code != tc.code || j.needBytes != tc.needBytes || j.text != tc.text {
				t.Fatalf("judgment=%+v", j)
			}
			if j.code != "" && (j.text != "" || j.extracts != 0) {
				t.Fatalf("a refusal kept text: %+v", j)
			}
		})
	}
}

func TestJudgeBytes(t *testing.T) {
	for state, want := range map[string]string{workiq.BytesNotEncrypted: "", workiq.BytesEncrypted: DocumentProtectionUnattributed,
		workiq.BytesUnknown: DocumentProtectionUnverifiable, "": DocumentProtectionUnverifiable} {
		if got := judgeBytes(state); got != want {
			t.Errorf("%q: %q", state, got)
		}
	}
}

func TestDocumentAccount(t *testing.T) {
	mail := workiq.AccountFact{ID: "outlook/p", Family: "mail"}
	a := workiq.AccountFact{ID: "tenant/a", Family: "chats", UserID: "a"}
	b := workiq.AccountFact{ID: "tenant/b", Family: "chats", UserID: "b"}
	if got, err := documentAccount([]workiq.AccountFact{mail, a}, ""); err != nil || got.ID != a.ID {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := documentAccount([]workiq.AccountFact{a, b}, "tenant/b"); err != nil || got.ID != b.ID {
		t.Fatalf("%v %v", got, err)
	}
	for _, tc := range []struct {
		facts    []workiq.AccountFact
		selector string
		code     string
	}{{[]workiq.AccountFact{a, b}, "", "workiq_account_required"}, {[]workiq.AccountFact{mail}, "", "workiq_chat_account_unmatched"},
		{[]workiq.AccountFact{a}, "outlook/p", "workiq_chat_account_unmatched"}} {
		if _, err := documentAccount(tc.facts, tc.selector); workIQOutcomeCode(err) != tc.code {
			t.Errorf("%v %q: %v", tc.facts, tc.selector, err)
		}
	}
}


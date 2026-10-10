package onedriveusage

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func escapedBody(t *testing.T, raw string) string {
	t.Helper()
	value, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(value[1 : len(value)-1])
}

func TestDecodeFormattedEscapedBody(t *testing.T) {
	raw := `{"file":{"FileName":"A \"quoted\" 世界 document","FileExtension":"docx","FileOwner":"Owner",
 "FileSize":0,"FileCreatedTime":"raw-created","FileModifiedTime":"raw-modified",
 "SharePointItem":{"SiteId":"site","WebId":"web","ListId":"list","UniqueId":"unique"},
 "Visualization":{"Title":"Separate visualization","AccessUrl":"https://example.invalid/a"},
 "ItemProperties":{"Shared":{"LastSharedWithMailboxOwnerBySmtp":"sharer@example.invalid",
 "LastSharedWithMailboxOwnerByDisplayName":"Sharer","LastSharedWithMailboxOwnerDateTime":"raw-latest",
 "TeamsMessageThreadId":"native-thread","AttachmentItemReferenceId":"attachment","SubjectProperty":"Latest subject"}},
 "AllExtensions":{"SharingHistory":{"Instances":[
 {"SharedByDisplayName":"Actor","SharedBySmtp":"actor@example.invalid","SharedByAadId":"aad",
 "SharedByTime":"raw-history","ConversationId":"conversation","Subject":"History subject","ParticipantsCount":1,
 "Participants":[{"DisplayName":"Participant","Smtp":"participant@example.invalid"}],
 "MeetingStartTime":"raw-meeting","ICalUid":"ical","MeetingSubject":"Meeting","isRecurring":false},
 {"SharedByTime":"another-raw-time","Subject":"C:\\fictional\\path"}]}}}}`
	zero, count, unrecurring := int64(0), int64(1), false
	want := DocumentObservation{
		ID: "native-id", Format: "variant", Title: "A \"quoted\" 世界 document", URL: "https://example.invalid/a",
		Extension: "docx", Owner: "Owner", Size: &zero, CreatedRaw: "raw-created", ModifiedRaw: "raw-modified",
		SiteID: "site", WebID: "web", ListID: "list", UniqueID: "unique",
		Latest: &Share{SMTP: "sharer@example.invalid", DisplayName: "Sharer", AtRaw: "raw-latest",
			ConversationID: "native-thread", AttachmentID: "attachment", Subject: "Latest subject"},
		History: []Share{
			{DisplayName: "Actor", SMTP: "actor@example.invalid", AadID: "aad", AtRaw: "raw-history",
				ConversationID: "conversation", Subject: "History subject", ParticipantsCount: &count,
				Participants:    []Person{{DisplayName: "Participant", SMTP: "participant@example.invalid"}},
				MeetingStartRaw: "raw-meeting", ICalUID: "ical", MeetingSubject: "Meeting", Recurring: &unrecurring},
			{AtRaw: "another-raw-time", Subject: `C:\fictional\path`},
		},
	}
	got, ok, err := decodeDocument("native-id", "variant", escapedBody(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded native evidence = %#v, %t; want %#v", got, ok, want)
	}
}

func TestDecodeCollaboratorDirectJSON(t *testing.T) {
	raw := `{"Id":"stated-user-id","DisplayName":"Person","EmailAddresses":["a@example.invalid","b@example.invalid"],
 "Department":"Department","JobTitle":"Title","OfficeLocation":"Office"}`
	got, ok, err := decodeCollaborator("source-row-id", raw)
	if err != nil {
		t.Fatal(err)
	}
	want := Collaborator{ID: "source-row-id", UserID: "stated-user-id", DisplayName: "Person",
		Emails: []string{"a@example.invalid", "b@example.invalid"}, Department: "Department", JobTitle: "Title", Office: "Office"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("collaborator = %#v,%t; want %#v", got, ok, want)
	}
}

func TestDecodeAmbiguousUnicodeTypesAndEnvelopeRefused(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"file":null}`, `{"file":{"FileName":3}}`, `{"file":{"FileSize":-1}}`,
		`{"file":{"FileSize":1.5}}`, `{"file":{"FileName":"a","FileName":"b"}}`,
		`{"file":{"SharePointItem":{"SiteId":123}}}`, `{"file":{"FileName":"\ud800"}}`,
		`{"file":{"AllExtensions":{"SharingHistory":{"Instances":{}}}}}`,
		`{"file":{"AllExtensions":{"SharingHistory":{"Instances":[{"Participants":["bad"]}]}}}}`,
		`{"file":{"AllExtensions":{"SharingHistory":{"Instances":[{"isRecurring":7}]}}}}`,
		`{"file":{}} {"file":{}}`,
	} {
		if got, ok, _ := decodeDocument("id", "format", escapedBody(t, raw)); ok || !reflect.DeepEqual(got, DocumentObservation{}) {
			t.Fatalf("ambiguous malformed document admitted: %#v,%t", got, ok)
		}

	}
	for _, raw := range []string{`{"EmailAddresses":[7]}`, `{"Id":3}`, `{"Id":"a","Id":"b"}`, `[]`} {
		if got, ok, _ := decodeCollaborator("id", raw); ok || !reflect.DeepEqual(got, Collaborator{}) {
			t.Fatalf("malformed collaborator admitted: %#v,%t", got, ok)
		}
	}
}

func TestDecodeMalformedEncodingAndOptionalEvidence(t *testing.T) {
	for _, body := range []string{
		`\uZZZZ`, `\ud800`, `bad"quote`, "\xff", `{\"file\":`,
	} {
		if got, ok, _ := decodeDocument("id", "f", body); ok || !reflect.DeepEqual(got, DocumentObservation{}) {
			t.Fatalf("invalid string-body encoding published: %#v,%t", got, ok)
		}
	}
	for _, raw := range []string{
		`{"file":[]}`, `{"file":{"FileSize":"1"}}`,
		`{"file":{"AllExtensions":{"SharingHistory":{"Instances":[7]}}}}`,
		`{"file":{"FileName":"bad\u0000title"}}`,
	} {
		if _, ok, _ := decodeDocument("id", "f", escapedBody(t, raw)); ok {
			t.Fatal("malformed native evidence admitted")
		}
	}
	got, ok, err := decodeDocument("id", "f", escapedBody(t, `{"file":{"Visualization":{"Title":"Fallback 😀"},
		 "FileSize":null,"ItemProperties":null,"AllExtensions":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Title != "Fallback 😀" || got.Size != nil || got.Latest != nil || len(got.History) != 0 {
		t.Fatalf("optional evidence = %#v,%t", got, ok)
	}
}

func TestDecodeJSONSyntaxAndStructureBounds(t *testing.T) {
	for _, raw := range []string{
		"", `{"`, `{"a":`, `{"a":1]`, `[1}`, `{"file":{}} {}`,
		"[\"bad\\", "[\"\\u12", `{"file":{"FileName":"\u12"}}`, `{"file":{"FileName":"\uZZZZ"}}`,
		`{"file":{"FileName":"\ud800\u0000"}}`, `{"file":{"FileName":"\ud800\uZZZZ"}}`,
		`{"file":{"FileName":"\ud800"}}`, "\xff",
		strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65),
		`{"extra":[` + strings.Repeat("0,", 131072) + `0]}`,
	} {
		if _, ok, _ := decodeCollaborator("id", raw); ok {
			t.Fatal("unsupported JSON syntax/structure admitted")
		}
	}
	got, ok, err := decodeCollaborator("id", `{"DisplayName":"Pair \ud83d\ude00","EmailAddresses":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.DisplayName != "Pair 😀" || len(got.Emails) != 0 {
		t.Fatalf("valid surrogate pair/empty aliases lost: %#v,%t", got, ok)
	}
}

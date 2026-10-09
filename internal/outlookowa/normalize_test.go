package outlookowa

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func TestNormalizeOpaqueIDsAndCoverage(t *testing.T) {
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	r := Result{RootID: "Root", Folders: []Folder{{ID: "z", ParentID: "Root", Value: outlookmail.Folder{Name: "Inbox", Kind: "inbox"}}, {ID: "A", ParentID: "z", Value: outlookmail.Folder{Kind: "other"}}}, Messages: []Message{{ID: "b", FolderID: "z", AttachmentIDs: []string{"a/=="}, Value: outlookmail.Message{Account: "Fixture", Header: outlookmail.Header{Received: newer}, Attachments: []outlookmail.Attachment{{Name: "synthetic.txt"}}}}, {ID: "B", FolderID: "z", Value: outlookmail.Message{Account: "Fixture", Header: outlookmail.Header{Received: old}}}}, SeenIDs: []string{"unmapped", "b", "B", "b"}, Notes: outlookmail.Notes{Messages: 2}, Losses: []outlookmail.Loss{{Code: outlookmail.CodeMailFolderMissing, Count: 1}}, Doubtful: true}
	var calls []string
	key := uint32(0)
	got, err := r.Normalize(func(kind, id string) (uint32, error) { calls = append(calls, kind+":"+id); key++; return key, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"attachment:a/==", "folder:A", "folder:Root", "folder:z", "message:B", "message:b", "message:unmapped"}) {
		t.Fatalf("allocation order: %v", calls)
	}
	if got.RootKey != 3 || len(got.Folders) != 2 || got.Folders[0].Key != 2 || got.Folders[0].Parent != 4 || got.Folders[1].Parent != 3 || !reflect.DeepEqual(got.SeenDetailKeys, []uint32{5, 6, 7}) {
		t.Fatalf("references: %+v", got)
	}
	if got.Messages[0].Detail.Key != 5 || got.Messages[0].Header.Key != 5 || got.Messages[0].DetailKey != 5 || got.Messages[0].FolderKey != 4 || got.Messages[0].Folder.Kind != "inbox" || got.Messages[1].Attachments[0].Key != 1 || got.Messages[1].Attachments[0].MessageKey != 6 || got.Messages[0].Account != "Fixture" {
		t.Fatal("message references")
	}
	if len(got.Coverage) != 1 || got.Coverage[0].FolderKey != 4 || got.Coverage[0].Count != 2 || !got.Coverage[0].Oldest.Equal(old) || !got.Coverage[0].Newest.Equal(newer) || !got.Doubtful || !reflect.DeepEqual(got.Notes, r.Notes) || !reflect.DeepEqual(got.Losses, r.Losses) {
		t.Fatalf("coverage/diagnostics: %+v", got)
	}
	if r.Folders[0].Value.Key != 0 || r.Messages[0].Value.Attachments[0].Key != 0 {
		t.Fatal("normalize mutated native result")
	}
}

func TestNormalizeRefusesResolverFailure(t *testing.T) {
	r := Result{SeenIDs: []string{"Opaque"}}
	sentinel := errors.New("synthetic resolver failure")
	got, err := r.Normalize(func(string, string) (uint32, error) { return 0, sentinel })
	if !errors.Is(err, sentinel) || len(got.SeenDetailKeys) != 0 {
		t.Fatalf("resolver failure = %v, %+v", err, got)
	}
}

func TestNormalizeValidation(t *testing.T) {
	for _, r := range []Result{
		{RootID: " "},
		{SeenIDs: []string{""}},
		{Folders: []Folder{{ID: "", ParentID: "R"}}},
		{Folders: []Folder{{ID: "F", ParentID: ""}}},
		{Messages: []Message{{ID: "", FolderID: "F"}}},
		{Messages: []Message{{ID: "M", FolderID: ""}}},
		{Messages: []Message{{ID: "M", FolderID: "F", AttachmentIDs: []string{"A"}}}},
		{Messages: []Message{{ID: "M", FolderID: "F", AttachmentIDs: []string{""}, Value: outlookmail.Message{Attachments: []outlookmail.Attachment{{}}}}}},
	} {
		calls := 0
		got, err := r.Normalize(func(string, string) (uint32, error) { calls++; return 1, nil })
		if err == nil || calls != 0 || len(got.Messages) != 0 {
			t.Fatal("invalid native IDs reached resolver")
		}
	}
	r := Result{SeenIDs: []string{"M"}}
	if _, err := r.Normalize(nil); err == nil {
		t.Fatal("nil resolver accepted")
	}
	if _, err := r.Normalize(func(string, string) (uint32, error) { return 0, nil }); err == nil {
		t.Fatal("zero numeric key accepted")
	}
	if got, err := (Result{}).Normalize(func(string, string) (uint32, error) { t.Fatal("empty ID resolved"); return 0, nil }); err != nil || got.RootKey != 0 {
		t.Fatal("empty source result")
	}
}

func TestNormalizeCoverageZeroAndResolverKeyOrder(t *testing.T) {
	r := Result{Folders: []Folder{{ID: "F", ParentID: "R", Value: outlookmail.Folder{Kind: "inbox"}}, {ID: "G", ParentID: "R", Value: outlookmail.Folder{Kind: "other"}}}, Messages: []Message{{ID: "A", FolderID: "F"}, {ID: "B", FolderID: "F", Value: outlookmail.Message{Header: outlookmail.Header{Received: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}}}, {ID: "C", FolderID: "F"}, {ID: "D", FolderID: "G"}}}
	got, err := r.Normalize(func(kind, id string) (uint32, error) {
		return map[string]uint32{"F": 2, "G": 3, "R": 1, "A": 9, "B": 8, "C": 7, "D": 10}[id], nil
	})
	if err != nil || got.Messages[0].Detail.Key != 7 || len(got.Coverage) != 2 || got.Coverage[0].Count != 3 || got.Coverage[0].Oldest.IsZero() || got.Coverage[0].Newest.IsZero() || !got.Coverage[1].Oldest.IsZero() {
		t.Fatal("key order or zero timestamp coverage")
	}
}

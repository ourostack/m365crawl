package outlookowa

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func TestCollectDistinguishedRootAnchor(t *testing.T) {
	folder := func(id, parent, typ string) any {
		return obj("id", id, "parentFolderId", parent, "distinguishedFolderType", typ, "displayName", "poison-msgfolderroot")
	}

	for _, tc := range []struct {
		name                string
		folders             []any
		messageFolder, root string
	}{
		{"anchor_ancillary", []any{folder("A", "OpaquePrimary", "MSGFOLDERROOT"), folder("I", "A", "inbox"), folder("S", "A", "sentitems"), folder("X", "OpaqueOther", "other"), folder("Y", "X", "other"), folder("Z", "Y", "other")}, "I", "OpaquePrimary"},
		{"legacy_unique", []any{folder("I", "OpaquePrimary", "inbox")}, "I", "OpaquePrimary"},
		{"legacy_ambiguous", []any{folder("I", "OpaquePrimary", "inbox"), folder("X", "OpaqueOther", "other")}, "I", ""},
		{"two_anchors", []any{folder("A", "R", "msgfolderroot"), folder("B", "A", "msgfolderroot"), folder("I", "A", "inbox")}, "I", ""},
		{"foreign_message", []any{folder("A", "R", "msgfolderroot"), folder("I", "A", "inbox"), folder("X", "Q", "other")}, "X", ""},
		{"same_external_not_descendant", []any{folder("A", "R", "msgfolderroot"), folder("I", "A", "inbox"), folder("X", "R", "other")}, "X", ""},
		{"foreign_inbox", []any{folder("A", "R", "msgfolderroot"), folder("I", "Q", "inbox")}, "A", ""},
		{"foreign_sent", []any{folder("A", "R", "msgfolderroot"), folder("S", "Q", "sent")}, "A", ""},
		{"ancillary_cycle", []any{folder("A", "R", "msgfolderroot"), folder("X", "Y", "other"), folder("Y", "X", "other")}, "A", ""},
		{"conflicting_anchor_row", []any{folder("A", "R", "msgfolderroot"), folder("A", "Q", "msgfolderroot"), folder("I", "A", "inbox")}, "I", ""},
		{"missing_parent", []any{folder("A", "", "msgfolderroot")}, "A", ""},
		{"unknown_message_folder", []any{folder("A", "R", "msgfolderroot")}, "Missing", ""},
		{"display_name_not_anchor", []any{folder("I", "R", "inbox"), folder("X", "Q", "other")}, "I", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := readerWith(header("M", tc.messageFolder))
			f.values[1] = tc.folders
			r, err := collect(context.Background(), f, "Synthetic", true)
			if err != nil || r.RootID != tc.root {
				t.Fatal("semantic_root_anchor_mismatch")
			}
			if tc.root == "" {
				if !r.Doubtful || !hasLoss(r, "outlook_owa_root_uncertain") {
					t.Fatal("ambiguous_anchor_trusted")
				}
			} else if r.Doubtful || len(r.Folders) != len(tc.folders) || len(r.Messages) != 1 {
				t.Fatal("anchored_data_dropped_or_doubtful")
			}
		})
	}
}

func TestFolderRootAnchorDirectGraphBoundaries(t *testing.T) {
	anchor := Folder{ID: "A", ParentID: "R", DistinguishedType: "msgfolderroot", Value: outlookmail.Folder{Kind: "other"}}
	if folderRoot(map[string]Folder{"A": anchor}, []Message{{FolderID: "Missing"}}) != "" {
		t.Fatal("unknown_message_folder_root_trusted")
	}
	if folderRoot(map[string]Folder{"A": {ID: "A", ParentID: " "}}, nil) != "" {
		t.Fatal("invalid_parent_root_trusted")
	}
}

func TestMapFolderAnchorExactFieldAndNormalizedValue(t *testing.T) {
	r := Result{}
	f, ok := mapFolder(obj("id", "A", "parentFolderId", "R", "displayName", "not-root", "distinguishedFolderType", "MsgFolderRoot"), &r)
	if !ok || r.Doubtful || f.DistinguishedType != "msgfolderroot" {
		t.Fatal("declared_anchor_not_retained")
	}
	r = Result{}
	f, ok = mapFolder(obj("id", "A", "parentFolderId", "R", "displayName", "msgfolderroot", "DistinguishedFolderType", "msgfolderroot"), &r)
	if !ok || f.DistinguishedType != "" {
		t.Fatal("inexact_field_or_displayname_selected_anchor")
	}
	r = Result{}
	f, ok = mapFolder(obj("id", "A", "parentFolderId", "R", "distinguishedFolderType", int64(1)), &r)
	if !ok || f.DistinguishedType != "" || !hasLoss(r, outlookmail.CodeMailUnmapped) {
		t.Fatal("malformed_anchor_metadata_loss_missing")
	}
}

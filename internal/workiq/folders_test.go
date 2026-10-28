package workiq

import (
	"context"
	"testing"
)

func TestWorkIQFolderProjectionDoesNotInventIdentityOrCounts(t *testing.T) {
	folder, err := DecodeFolder(context.Background(), []byte(`{"id":"folder-one","parentFolderId":"root","displayName":"Inbox",
"wellKnownName":"inbox","childFolderCount":2,"totalItemCount":0,"isHidden":true}`))
	if err != nil || folder.ID != "folder-one" || folder.ParentID != "root" || folder.Name != "Inbox" || folder.WellKnown != "" ||
		folder.ChildCount == nil || *folder.ChildCount != 2 || folder.TotalCount == nil || *folder.TotalCount != 0 || folder.Hidden == nil || !*folder.Hidden {
		t.Fatal("folder identity or nullable totals were guessed from names or missing values")
	}
	minimal, err := DecodeFolder(context.Background(), []byte(`{"id":"minimal"}`))
	if err != nil || minimal.TotalCount != nil || minimal.ChildCount != nil || minimal.Hidden != nil {
		t.Fatal("missing folder metadata became measured zero or false")
	}
	for _, raw := range []string{
		`{`, `{}`, `{"id":"one","childFolderCount":-1}`, `{"id":"one","totalItemCount":-1}`,
		`{"id":"one","totalItemCount":999999999999999999999999999999}`,
	} {
		_, err := DecodeFolder(context.Background(), []byte(raw))
		requireCode(t, err, "workiq_protocol")
	}
}

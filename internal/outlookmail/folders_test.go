package outlookmail

import (
	"slices"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// A message with header copies in several folders lists every folder once, with the read state of
// the newest copy there (#270): a message sent to oneself is read in Sent Items and unread in Inbox.
func TestCollectListsEveryFolderOfAMessage(t *testing.T) {
	read := func(key, detail, folder uint32, stamp uint64) *hxbuild.Object {
		return hdr(key, detail, folder, stamp, "self", 2)
	}
	sentRead := hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: 13, Stamp: 5, DetailKey: 21, FolderKey: fSent, Received: at(2, 9), Subject: "self", Unread: 0})
	objs := append(folderObjs(),
		read(11, 21, fToMe, 1), read(12, 21, fInbox, 9), sentRead, read(14, 21, fInbox, 2), det(21, "<a@example.invalid>"),
		read(15, 22, fInbox, 1), det(22, "<b@example.invalid>"),
	)
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 2 {
		t.Fatalf("%d", len(r.Messages))
	}
	m := r.Messages[0]
	if m.Folder.Key != fInbox || m.Copies != 4 {
		t.Fatalf("%+v", m)
	}
	var keys []uint32
	unread := map[uint32]bool{}
	for _, c := range m.FolderCopies {
		keys = append(keys, c.Key)
		if c.Unread == nil {
			t.Fatalf("no read state for %d", c.Key)
		}
		unread[c.Key] = *c.Unread
	}
	if !slices.Equal(keys, []uint32{fInbox, fSent, fToMe}) || unread[fSent] || !unread[fInbox] || !unread[fToMe] {
		t.Fatalf("%v %v", keys, unread)
	}
	if one := r.Messages[1].FolderCopies; len(one) != 1 || one[0].Key != fInbox {
		t.Fatalf("%+v", one)
	}
}

// A message kept only through copies in folders the store does not hold lists those folders.
func TestCollectFolderCopiesOfAnUnknownFolderMessage(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, 999, 1, "x", 2), det(21, "<a@example.invalid>"), hdr(12, 22, fInbox, 1, "y", 2), det(22, "<b@example.invalid>"))
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	for _, m := range r.Messages {
		if m.DetailKey == 21 && (len(m.FolderCopies) != 1 || m.FolderCopies[0].Key != 999) {
			t.Fatalf("%+v", m.FolderCopies)
		}
	}
}

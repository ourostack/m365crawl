package outlookmail

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// The folder set of the account under test: the root key 1000 holds an inbox, a sent folder, a
// To Me folder and a junk folder (the last two share type 0x7a), and deleted items.
const (
	rootKey   = 1000
	fInbox    = 101
	fSent     = 102
	fToMe     = 103
	fJunk     = 104
	fDeleted  = 105
	otherRoot = 2000
	fOtherBox = 201
)

func folderObjs() []*hxbuild.Object {
	f := func(key uint32, name string, typ uint32) *hxbuild.Object {
		return hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: key, Parent: rootKey, Name: name, Type: typ})
	}
	return []*hxbuild.Object{
		f(fInbox, "Fixture Inbox", 0x61), f(fSent, "Fixture Sent", 0x65), f(fToMe, "Fixture To Me", 0x7a),
		f(fJunk, "Fixture Junk", 0x7a), f(fDeleted, "Fixture Deleted", 0x67),
	}
}

func hdr(key, detail, folder uint32, stamp uint64, subject string, day int) *hxbuild.Object {
	return hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{
		Key: key, Stamp: stamp, DetailKey: detail, FolderKey: folder, Received: at(day, 9),
		Subject: subject, SenderName: "Fixture Sender", SenderAddr: "sender@example.invalid", Unread: 1, Importance: 1,
	})
}

func det(key uint32, msgID string) *hxbuild.Object {
	return hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: key, MessageID: msgID, Class: "IPM.Note", Sent: at(1, 8)})
}

func framed(objs ...*hxbuild.Object) []byte { return hxbuild.FramedPayload(hxbuild.Head(15), objs...) }

func openStore(t *testing.T, data []byte) *hxstore.Store {
	t.Helper()
	s, err := hxstore.OpenStore(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// storeOf builds a store with one block per payload.
func storeOf(t *testing.T, payloads ...[]byte) *hxstore.Store {
	t.Helper()
	b := hxbuild.New(hxbuild.Options{})
	for _, p := range payloads {
		b.BlockCodec(p, hxbuild.CodecLiteral)
	}
	return openStore(t, b.Bytes())
}

func collect(t *testing.T, s *hxstore.Store, o Options) Result {
	t.Helper()
	r, err := Collect(context.Background(), s, t.TempDir(), "outlook/Test", o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func lossOf(r Result, code string) int {
	for _, l := range r.Losses {
		if l.Code == code {
			return l.Count
		}
	}
	return 0
}

func TestCollectJoinsCopiesOfOneMessage(t *testing.T) {
	objs := append(folderObjs(),
		hdr(11, 21, fToMe, 1, "Fixture subject", 2), hdr(12, 21, fInbox, 9, "Fixture subject", 2), hdr(13, 21, fSent, 5, "Fixture subject", 2),
		det(21, "<a@example.invalid>"),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 31, Parent: 21, Name: "Fixture One", Address: "one@example.invalid", Kind: 2}),
		hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 21, HTML: []byte("<p>fixture</p>")}),
	)
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 1 || r.Notes.Messages != 1 || r.Notes.HeadersSeen != 3 {
		t.Fatalf("%+v", r.Notes)
	}
	m := r.Messages[0]
	if m.Account != "outlook/Test" || m.DetailKey != 21 || m.Folder.Key != fInbox || m.Folder.Kind != "inbox" || !m.ToMe || m.Copies != 3 {
		t.Fatalf("%+v", m)
	}
	if m.MessageID != "<a@example.invalid>" || m.Subject != "Fixture subject" || m.Body.State != BodyInline || string(m.Body.HTML) != "<p>fixture</p>" {
		t.Fatalf("%+v", m)
	}
	if len(m.Recipients) != 1 || m.Recipients[0].Address != "one@example.invalid" {
		t.Fatalf("%+v", m.Recipients)
	}
	if len(r.Folders) != 5 || r.Notes.BodiesInline != 1 || len(r.Losses) != 0 {
		t.Fatalf("%+v %+v", r.Folders, r.Losses)
	}
	kinds := map[uint32]string{}
	for _, f := range r.Folders {
		kinds[f.Key] = f.Kind
	}
	if kinds[fToMe] != "to_me" || kinds[fJunk] != "junk" || kinds[fInbox] != "inbox" || kinds[fSent] != "sent" || kinds[fDeleted] != "deleted" {
		t.Fatalf("%v", kinds)
	}
}

func TestCollectToMeOnlyKeepsTheToMeFolder(t *testing.T) {
	// Another message sits in Inbox and To Me, so To Me is told from Junk; this one has only a
	// To Me copy and is shown from it.
	objs := append(folderObjs(),
		hdr(11, 21, fToMe, 1, "both", 2), hdr(12, 21, fInbox, 2, "both", 2), det(21, "<a@example.invalid>"),
		hdr(13, 22, fToMe, 3, "only to me", 3), hdr(14, 22, fToMe, 4, "only to me", 3), det(22, "<b@example.invalid>"),
	)
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 2 {
		t.Fatalf("%d", len(r.Messages))
	}
	m := r.Messages[1]
	if m.DetailKey != 22 || m.Folder.Kind != "to_me" || !m.ToMe || m.Copies != 2 || m.Header.Key != 14 {
		t.Fatalf("%+v", m)
	}
}

func TestCollectClassifyToMeNeedsSharedInboxKeys(t *testing.T) {
	// One 0x7a folder shares detail keys with the Inbox, the other holds mail of its own.
	objs := append(folderObjs(),
		hdr(11, 21, fToMe, 1, "x", 2), hdr(12, 21, fInbox, 2, "x", 2), det(21, "<a@example.invalid>"),
		hdr(13, 22, fJunk, 1, "y", 2), det(22, "<b@example.invalid>"),
	)
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	byDetail := map[uint32]Message{}
	for _, m := range r.Messages {
		byDetail[m.DetailKey] = m
	}
	if byDetail[21].Folder.Kind != "inbox" || !byDetail[21].ToMe || byDetail[22].Folder.Kind != "junk" || byDetail[22].ToMe {
		t.Fatalf("%+v %+v", byDetail[21], byDetail[22])
	}
}

func TestCollectNewerStampAtLowerOffsetWins(t *testing.T) {
	folders := folderObjs()
	// The Sent copy has the higher stamp but sits in the earlier block.
	first := framed(append(folders, hdr(11, 21, fSent, 9, "newer", 2), det(21, "<a@example.invalid>"))...)
	second := framed(hdr(12, 21, fInbox, 5, "older", 2))
	r := collect(t, storeOf(t, first, second), Options{})
	if len(r.Messages) != 1 || r.Messages[0].Folder.Key != fSent || r.Messages[0].Subject != "newer" {
		t.Fatalf("%+v", r.Messages[0])
	}
}

func TestVersionRuleOrder(t *testing.T) {
	base := version{stamp: 5, block: 100, pos: 10}
	for name, tc := range map[string]struct {
		a, b version
		want bool
	}{
		"higher stamp": {version{6, 0, 0}, base, true}, "lower stamp": {version{4, 999, 999}, base, false},
		"same stamp, later block": {version{5, 200, 0}, base, true}, "same stamp, earlier block": {version{5, 50, 99}, base, false},
		"same block, later position": {version{5, 100, 11}, base, true}, "same block, earlier position": {version{5, 100, 9}, base, false},
		"identical": {base, base, false},
	} {
		if got := tc.a.newer(tc.b); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestCollectVersionPerClass(t *testing.T) {
	// Two copies of every class; the one with the higher stamp wins, whatever the file order.
	objs := append(folderObjs(),
		hdr(11, 21, fInbox, 9, "header new", 2), hdr(11, 21, fInbox, 3, "header old", 2),
		hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 21, Stamp: 2, MessageID: "<new@example.invalid>"}),
		hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 21, Stamp: 1, MessageID: "<old@example.invalid>"}),
		hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 21, Stamp: 1, HTML: []byte("<p>old</p>")}),
		hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 21, Stamp: 2, HTML: []byte("<p>new</p>")}),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 31, Stamp: 2, Parent: 21, Name: "New Name"}),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 31, Stamp: 1, Parent: 21, Name: "Old Name"}),
		hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 41, Stamp: 1, MessageKey: 21, Name: "old.txt"}),
		hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 41, Stamp: 2, MessageKey: 21, Name: "new.txt"}),
		hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: fInbox, Stamp: 5, Parent: rootKey, Name: "Newer Inbox", Type: 0x61}),
	)
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	m := r.Messages[0]
	if m.Subject != "header new" || m.MessageID != "<new@example.invalid>" || string(m.Body.HTML) != "<p>new</p>" || m.Folder.Name != "Newer Inbox" {
		t.Fatalf("%+v", m)
	}
	if len(m.Recipients) != 1 || m.Recipients[0].Name != "New Name" || len(m.Attachments) != 1 || m.Attachments[0].Name != "new.txt" || m.Copies != 1 {
		t.Fatalf("%+v %+v", m.Recipients, m.Attachments)
	}
}

func TestCollectVersionTiesBreakByBlockThenPosition(t *testing.T) {
	folders := folderObjs()
	a := framed(append(folders, hdr(11, 21, fInbox, 4, "block one first", 2), hdr(11, 21, fInbox, 4, "block one second", 2), det(21, "<a@example.invalid>"))...)
	r := collect(t, storeOf(t, a), Options{})
	if r.Messages[0].Subject != "block one second" {
		t.Fatalf("position: %q", r.Messages[0].Subject)
	}
	b := framed(hdr(11, 21, fInbox, 4, "block two", 2))
	r = collect(t, storeOf(t, a, b), Options{})
	if r.Messages[0].Subject != "block two" {
		t.Fatalf("block: %q", r.Messages[0].Subject)
	}
}

func TestCollectMissingDetailSkipsTheMessageAndIsNoLoss(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "x", 2), hdr(12, 21, fSent, 1, "x", 2), hdr(13, 22, fInbox, 1, "y", 2), det(22, "<b@example.invalid>"))
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 1 || r.Messages[0].DetailKey != 22 || r.Notes.MissingDetail != 1 || len(r.Losses) != 0 || !slices.Equal(r.SeenDetailKeys, []uint32{21, 22}) {
		t.Fatalf("%+v %+v", r.Notes, r.Losses)
	}
}

func TestCollectOrphans(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "x", 2), det(21, "<a@example.invalid>"),
		hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 42, MessageKey: 21, Name: "b.txt", Size: 2}),
		hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 41, MessageKey: 21, Name: "a.txt", Size: 1}),
		hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 43, MessageKey: 999, Name: "lost.txt"}),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 52, Parent: 21, Name: "Two"}),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 51, Parent: 21, Name: "One"}),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 53, Parent: 999, Name: "Lost"}),
	)
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	m := r.Messages[0]
	if r.Notes.OrphanAttachments != 1 || r.Notes.OrphanRecipients != 1 || len(r.Losses) != 0 {
		t.Fatalf("%+v %+v", r.Notes, r.Losses)
	}
	if len(m.Attachments) != 2 || m.Attachments[0].Name != "a.txt" || m.Attachments[1].Name != "b.txt" {
		t.Fatalf("%+v", m.Attachments)
	}
	if len(m.Recipients) != 2 || m.Recipients[0].Name != "One" || m.Recipients[1].Name != "Two" {
		t.Fatalf("%+v", m.Recipients)
	}
}

func TestCollectKeepsOnlyTheAccountsFolders(t *testing.T) {
	// A second folder set has fewer messages; its folders and messages are left out and counted.
	other := hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: fOtherBox, Parent: otherRoot, Name: "Other Inbox", Type: 0x61})
	objs := append(folderObjs(), other,
		hdr(11, 21, fInbox, 1, "mine", 2), det(21, "<a@example.invalid>"),
		hdr(12, 22, fSent, 1, "mine too", 2), det(22, "<b@example.invalid>"),
		hdr(13, 23, fOtherBox, 1, "theirs", 2), det(23, "<c@example.invalid>"),
	)
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 2 || len(r.Folders) != 5 || r.Notes.OtherRoot != 1 || len(r.Losses) != 0 {
		t.Fatalf("%d messages %d folders %+v", len(r.Messages), len(r.Folders), r.Notes)
	}
	for _, f := range r.Folders {
		if f.Key == fOtherBox {
			t.Fatal("the other account's folder is listed")
		}
	}
}

func TestAccountRootChoice(t *testing.T) {
	f := func(key, parent uint32) Folder { return Folder{Key: key, Parent: parent, Kind: "inbox"} }
	folders := map[uint32]Folder{1: f(1, 100), 2: f(2, 100), 3: f(3, 200), 4: f(4, 3), 5: f(5, 4)}
	// The most header copies decide.
	if got := accountRoot(folders, []Header{{FolderKey: 3}, {FolderKey: 5}, {FolderKey: 1}}); got != 200 {
		t.Fatalf("by headers: %d", got)
	}
	// With no headers the root with more folders wins (a nested folder belongs to its root).
	if got := accountRoot(folders, nil); got != 200 {
		t.Fatalf("by folders: %d", got)
	}
	// A tie goes to the lower key; a header naming no folder counts for nothing.
	tied := map[uint32]Folder{1: f(1, 300), 2: f(2, 100)}
	if got := accountRoot(tied, []Header{{FolderKey: 1}, {FolderKey: 2}, {FolderKey: 99}}); got != 100 {
		t.Fatalf("tie: %d", got)
	}
	if got := accountRoot(nil, nil); got != 0 {
		t.Fatalf("empty: %d", got)
	}
	// A loop of parents ends and does not hang.
	loop := map[uint32]Folder{1: f(1, 2), 2: f(2, 1)}
	if rootOf(loop, 1) == 0 && len(loop) == 0 {
		t.Fatal("unreachable")
	}
	_ = rootOf(loop, 1)
}

func TestCollectMissingFolderIsANoteNotALoss(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, 999, 1, "x", 2), det(21, "<a@example.invalid>"), hdr(12, 22, fInbox, 1, "y", 2), det(22, "<b@example.invalid>"))
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 1 || r.Notes.MissingFolder != 1 || len(r.Losses) != 0 || !slices.Equal(r.SeenDetailKeys, []uint32{21, 22}) {
		t.Fatalf("%+v %+v", r.Notes, r.Losses)
	}
}

func TestCollectCoverage(t *testing.T) {
	objs := append(folderObjs(),
		hdr(11, 21, fInbox, 1, "a", 5), det(21, "<a@example.invalid>"),
		hdr(12, 22, fInbox, 1, "b", 3), det(22, "<b@example.invalid>"),
		hdr(13, 23, fInbox, 1, "c", 9), det(23, "<c@example.invalid>"),
		hdr(14, 24, fSent, 1, "d", 4), det(24, "<d@example.invalid>"),
		// A message with copies in Inbox and To Me counts in both folders; one with no received time
		// counts but leaves the range alone.
		hdr(15, 25, fInbox, 2, "e", 6), hdr(16, 25, fToMe, 1, "e", 6), det(25, "<e@example.invalid>"),
	)
	noTime := hdr(17, 26, fSent, 1, "f", 1)
	noTime.PutU64(224, 0)
	objs = append(objs, noTime, det(26, "<f@example.invalid>"))
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	got := map[uint32]Coverage{}
	for _, c := range r.Coverage {
		got[c.FolderKey] = c
	}
	in, sent, tome := got[fInbox], got[fSent], got[fToMe]
	if in.Count != 4 || !in.Oldest.Equal(at(3, 9)) || !in.Newest.Equal(at(9, 9)) {
		t.Fatalf("inbox %+v", in)
	}
	if sent.Count != 2 || !sent.Oldest.Equal(at(4, 9)) || !sent.Newest.Equal(at(4, 9)) {
		t.Fatalf("sent %+v", sent)
	}
	if tome.Count != 1 || !tome.Oldest.Equal(at(6, 9)) || len(r.Coverage) != 3 {
		t.Fatalf("to me %+v of %d", tome, len(r.Coverage))
	}
	if r.Coverage[0].FolderKey >= r.Coverage[1].FolderKey {
		t.Fatal("coverage is not sorted by folder")
	}
}

func TestCollectContextCancelled(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "x", 2), det(21, "<a@example.invalid>"))
	s := storeOf(t, framed(objs...))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx, s, t.TempDir(), "outlook/Test", Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

// cancelAfterWalk cancels the context once the walk is done, so the later stages see it.
func TestCollectContextCancelledAfterTheWalk(t *testing.T) {
	root := t.TempDir()
	writeDat(t, root, "S0/2/EFMData/1.dat", gz(t, []byte("<p>x</p>")))
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "x", 2), det(21, "<a@example.invalid>"),
		hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 21, Path: datPath}))
	s := storeOf(t, framed(objs...))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := afterWalk
	afterWalk = cancel
	defer func() { afterWalk = old }()
	if _, err := Collect(ctx, s, root, "outlook/Test", Options{ReadBodies: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestCollectBadStringsAreBlankedAndCounted(t *testing.T) {
	bad := hdr(11, 21, fInbox, 1, "x", 2)
	bad.PutU32(904, 2|1<<31) // a subject that does not end in a terminator
	badFolder := hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 150, Parent: rootKey, Name: "x"})
	badFolder.PutU32(1092, 2|1<<31)
	badRec := hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 60, Parent: 21, Name: "x"})
	badRec.PutU32(296, 2|1<<31)
	badAtt := hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 61, MessageKey: 21, Name: "x"})
	badAtt.PutU32(612, 2|1<<31)
	badDet := hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 21, MessageID: "<c@example.invalid>"})
	badDet.PutU32(1232, 2|1<<31)
	objs := append(folderObjs(), badFolder, bad, badDet, badRec, badAtt)
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 1 || r.Notes.BadString != 5 || r.Notes.Unmapped != 0 || len(r.Losses) != 0 {
		t.Fatalf("%d messages %+v %+v", len(r.Messages), r.Notes, r.Losses)
	}
	m := r.Messages[0]
	if m.Subject != "" || m.MessageID != "" || m.Header.Key != 11 || len(m.Recipients) != 1 || m.Recipients[0].Name != "" || len(m.Attachments) != 1 || m.Attachments[0].Name != "" {
		t.Fatalf("%+v", m)
	}
}

func TestCollectUnmappedObjectsAreALoss(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "x", 2), det(21, "<a@example.invalid>"),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 60, Parent: 21}),
		hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 61, MessageKey: 21}))
	s := storeOf(t, framed(objs...))
	fail := func(string) error { return unmapped("test") }
	type seam struct {
		name string
		swap func() func()
	}
	for _, sm := range []seam{
		{"folder", func() func() {
			o := mapFolder
			mapFolder = func(hxstore.Object) (Folder, error) { return Folder{}, fail("") }
			return func() { mapFolder = o }
		}},
		{"header", func() func() {
			o := mapHeader
			mapHeader = func(hxstore.Object) (Header, error) { return Header{}, fail("") }
			return func() { mapHeader = o }
		}},
		{"recipient", func() func() {
			o := mapRecipient
			mapRecipient = func(hxstore.Object) (uint32, Recipient, error) { return 0, Recipient{}, fail("") }
			return func() { mapRecipient = o }
		}},
		{"attachment", func() func() {
			o := mapAttachment
			mapAttachment = func(hxstore.Object) (Attachment, error) { return Attachment{}, fail("") }
			return func() { mapAttachment = o }
		}},
		{"detail", func() func() {
			o := mapDetail
			mapDetail = func(hxstore.Object) (Detail, error) { return Detail{}, fail("") }
			return func() { mapDetail = o }
		}},
	} {
		restore := sm.swap()
		r := collect(t, s, Options{})
		restore()
		if r.Notes.Unmapped == 0 || lossOf(r, CodeMailUnmapped) != r.Notes.Unmapped {
			t.Errorf("%s: %+v %+v", sm.name, r.Notes, r.Losses)
		}
	}
}

func TestCollectResyncedObjectsAreSkippedAndNoLoss(t *testing.T) {
	objs := append(folderObjs(), hdr(12, 22, fInbox, 1, "ok", 2), det(22, "<b@example.invalid>"))
	// One object follows three stray bytes: it is reached after unknown framing and is skipped.
	stray := append([]byte{1, 2, 3}, hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: 14, DetailKey: 22, FolderKey: fInbox}).Encode()...)
	r := collect(t, storeOf(t, append(framed(objs...), stray...)), Options{})
	if len(r.Messages) != 1 || r.Messages[0].Copies != 1 || r.Notes.ResyncedSkipped != 1 || len(r.Losses) != 0 {
		t.Fatalf("%+v %+v", r.Notes, r.Losses)
	}
}

func TestCollectToMeWithTheHighestStampStillShowsTheInbox(t *testing.T) {
	// The To Me copy is the newest, the Inbox and Sent copies are older: the message is shown
	// from the Inbox (the newest copy outside To Me) and is marked to_me.
	objs := append(folderObjs(),
		hdr(11, 21, fToMe, 99, "x", 2), hdr(12, 21, fInbox, 7, "x", 2), hdr(13, 21, fSent, 3, "x", 2), det(21, "<a@example.invalid>"))
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	m := r.Messages[0]
	if m.Folder.Key != fInbox || m.Folder.Kind != "inbox" || !m.ToMe || m.Copies != 3 || m.Header.Key != 12 {
		t.Fatalf("%+v", m)
	}
}

func TestCollectRootKey(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "x", 2), det(21, "<a@example.invalid>"))
	if r := collect(t, storeOf(t, framed(objs...)), Options{}); r.RootKey != rootKey {
		t.Fatalf("%d", r.RootKey)
	}
}

func TestCollectOrphansAreOnlyUnknownParents(t *testing.T) {
	// The message in another account's folder is skipped; its children are not orphans, because
	// their parent exists. Children of a key with no detail object are.
	objs := append(folderObjs(), hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: fOtherBox, Parent: otherRoot, Name: "Other", Type: 0x61}),
		hdr(11, 21, fInbox, 1, "x", 2), det(21, "<a@example.invalid>"),
		hdr(12, 22, fOtherBox, 1, "theirs", 2), det(22, "<b@example.invalid>"),
		hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 61, MessageKey: 22, Name: "kept.txt"}),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 62, Parent: 22, Name: "Kept"}),
		hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 63, MessageKey: 999, Name: "lost.txt"}),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 64, Parent: 999, Name: "Lost"}))
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if r.Notes.OtherRoot != 1 || r.Notes.OrphanAttachments != 1 || r.Notes.OrphanRecipients != 1 {
		t.Fatalf("%+v", r.Notes)
	}
}

func TestCollectGuardTagDrift(t *testing.T) {
	for name, o := range map[string]*hxbuild.Object{
		"header": hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Tag: 0x42f, Key: 11}),
		"detail": hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Tag: 0x60e, Key: 21}),
		"body":   hxbuild.NewMailBody(hxbuild.MailBodySpec{Tag: 0x74d, Key: 21}),
	} {
		objs := append(folderObjs(), hdr(10, 20, fInbox, 1, "x", 2), det(20, "<a@example.invalid>"), o)
		r, err := Collect(context.Background(), storeOf(t, framed(objs...)), t.TempDir(), "outlook/Test", Options{})
		var g *hxstore.GuardError
		if !errors.As(err, &g) || g.Code != CodeMailLayoutUnsupported || !strings.Contains(g.Detail, " x1, known 0x") || len(r.Messages) != 0 {
			t.Errorf("%s: %v %d", name, err, len(r.Messages))
		}
	}
	// Two unknown tags of one class are both named, in order.
	objs := append(folderObjs(),
		hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Tag: 0x42e, Key: 11}), hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Tag: 0x42f, Key: 12}),
		hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Tag: 0x42f, Key: 13}), hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Tag: 0x60e, Key: 21}))
	_, err := Collect(context.Background(), storeOf(t, framed(objs...)), t.TempDir(), "outlook/Test", Options{})
	var g *hxstore.GuardError
	if !errors.As(err, &g) || g.Detail != "class 0x4f tag 0x42e x1, known 0x430; class 0x4f tag 0x42f x2, known 0x430; class 0xc9 tag 0x60e x1, known 0x60f" {
		t.Fatalf("%v", err)
	}
}

func TestCollectOtherClassTagDriftIsALoss(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "x", 2), det(21, "<a@example.invalid>"),
		hxbuild.NewAttachment(hxbuild.AttachmentSpec{Tag: 0x317, Key: 41, MessageKey: 21}),
		hxbuild.NewRecipient(hxbuild.RecipientSpec{Tag: 0x15d, Key: 51, Parent: 21}),
		hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Tag: 0x4cf, Key: 150, Parent: rootKey}))
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 1 || r.Notes.OtherTagSkipped != 3 || lossOf(r, CodeMailLayoutPartial) != 3 || len(r.Messages[0].Attachments) != 0 {
		t.Fatalf("%+v %+v", r.Notes, r.Losses)
	}
}

func TestCollectGuardNoHeaders(t *testing.T) {
	s := storeOf(t, framed(folderObjs()...))
	_, err := Collect(context.Background(), s, t.TempDir(), "outlook/Test", Options{ExpectMail: true})
	var g *hxstore.GuardError
	if !errors.As(err, &g) || g.Code != CodeMailLayoutUnsupported || g.Detail != "no_mail_objects" {
		t.Fatalf("%v", err)
	}
	// Without the expectation an empty mailbox is read as empty.
	r := collect(t, s, Options{})
	if len(r.Messages) != 0 || len(r.Folders) != 5 {
		t.Fatalf("%+v", r)
	}
}

func TestCollectGuardWalkCoverage(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "x", 2), det(21, "<a@example.invalid>"))
	p := framed(objs...)
	// Bytes that are no object, no head and no trailer: the share the walk covers falls.
	for fill, wantRefused := range map[int]bool{len(p) / 6: false, len(p): true} {
		junk := bytes.Repeat([]byte{0xAA}, fill)
		_, err := Collect(context.Background(), storeOf(t, append(append([]byte(nil), p...), junk...)), t.TempDir(), "outlook/Test", Options{})
		var g *hxstore.GuardError
		if got := errors.As(err, &g) && g.Detail == "walk_coverage"; got != wantRefused {
			t.Errorf("fill %d of %d: refused=%v, err %v", fill, len(p), got, err)
		}
	}
	// Exactly at 79% and 81% covered.
	for pct, wantRefused := range map[int]bool{79: true, 81: false} {
		junk := bytes.Repeat([]byte{0xAA}, len(p)*(100-pct)/pct)
		_, err := Collect(context.Background(), storeOf(t, append(append([]byte(nil), p...), junk...)), t.TempDir(), "outlook/Test", Options{})
		var g *hxstore.GuardError
		if got := errors.As(err, &g) && g.Detail == "walk_coverage"; got != wantRefused {
			t.Errorf("%d%%: refused=%v, err %v", pct, got, err)
		}
	}
}

func TestCollectDamagedBlocksAreALoss(t *testing.T) {
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "x", 2), det(21, "<a@example.invalid>"))
	b := hxbuild.New(hxbuild.Options{})
	b.BlockCodec(framed(objs...), hxbuild.CodecLiteral)
	b.Raw(hxbuild.BadPayloadCRC(hxbuild.EncodeBlock(hxbuild.BlockTypeData, framed(hdr(12, 22, fInbox, 1, "y", 2)))))
	r := collect(t, openStore(t, b.Bytes()), Options{})
	if len(r.Messages) != 1 || lossOf(r, CodeBlocksDamaged) != 1 {
		t.Fatalf("%+v", r.Losses)
	}
	if r.Stats.BlocksFound != 2 {
		t.Fatalf("%+v", r.Stats)
	}
}

func TestCollectBodies(t *testing.T) {
	root := t.TempDir()
	writeDat(t, root, "S0/2/EFMData/1.dat", gz(t, []byte("<p>from file</p>")))
	writeDat(t, root, "S0/2/EFMData/3.dat", []byte("not gzip"))
	writeDat(t, root, "S0/2/EFMData/4.dat", gz(t, append([]byte("<p>"), 0xff, 0xfe)))
	writeDat(t, root, "S0/2/EFMData/5.dat", gz(t, bytes.Repeat([]byte("<p>"), 100)))
	file := func(n string) string { return "~/Files/S0/2/EFMData/" + n + ".dat" }
	objs := append(folderObjs(),
		hdr(11, 21, fInbox, 1, "file", 2), det(21, "<a@example.invalid>"), hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 21, Path: file("1")}),
		hdr(12, 22, fInbox, 1, "gone", 2), det(22, "<b@example.invalid>"), hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 22, Path: file("2")}),
		hdr(13, 23, fInbox, 1, "bad gzip", 2), det(23, "<c@example.invalid>"), hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 23, Path: file("3")}),
		hdr(14, 24, fInbox, 1, "bad utf8", 2), det(24, "<d@example.invalid>"), hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 24, Path: file("4")}),
		hdr(15, 25, fInbox, 1, "too big", 2), det(25, "<e@example.invalid>"), hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 25, Path: file("5")}),
		hdr(16, 26, fInbox, 1, "no body object", 2), det(26, "<f@example.invalid>"),
		hdr(17, 27, fInbox, 1, "empty body", 2), det(27, "<g@example.invalid>"), hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 27}),
		hdr(18, 28, fInbox, 1, "inline not utf8", 2), det(28, "<h@example.invalid>"), hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: 28, HTML: []byte{'<', 0xff}}),
	)
	s := storeOf(t, framed(objs...))
	byDetail := func(r Result) map[uint32]Message {
		out := map[uint32]Message{}
		for _, m := range r.Messages {
			out[m.DetailKey] = m
		}
		return out
	}
	// Not asked to read: a file body keeps its path and no bytes.
	r, err := Collect(context.Background(), s, root, "outlook/Test", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m := byDetail(r)[21]; m.Body.State != BodyNotRead || m.Body.HTML != nil || m.Body.Path != file("1") || r.Notes.BodiesNotRead != 5 {
		t.Fatalf("%+v %+v", m.Body, r.Notes)
	}
	// Asked to read, with a cap of 100 bytes.
	r, err = Collect(context.Background(), s, root, "outlook/Test", Options{ReadBodies: true, MaxBody: 100})
	if err != nil {
		t.Fatal(err)
	}
	b := byDetail(r)
	if b[21].Body.State != BodyFile || string(b[21].Body.HTML) != "<p>from file</p>" {
		t.Fatalf("file: %+v", b[21].Body)
	}
	for k, want := range map[uint32]string{22: BodyMissing, 23: BodyUnreadable, 24: BodyUnreadable, 25: BodyUnreadable, 26: BodyNone, 27: BodyNone, 28: BodyUnreadable} {
		if b[k].Body.State != want || b[k].Body.HTML != nil {
			t.Errorf("%d: %+v", k, b[k].Body)
		}
	}
	n := r.Notes
	if n.BodiesFile != 1 || n.BodiesMissing != 1 || n.BodiesUnreadable != 4 || n.BodiesNone != 2 || n.BodiesNotRead != 0 {
		t.Fatalf("%+v", n)
	}
	// Only the messages that need a body are read.
	r, err = Collect(context.Background(), s, root, "outlook/Test", Options{ReadBodies: true, NeedBody: func(k uint32) bool { return k == 21 }})
	if err != nil {
		t.Fatal(err)
	}
	if r.Notes.BodiesFile != 1 || r.Notes.BodiesNotRead != 4 || r.Notes.BodiesMissing != 0 {
		t.Fatalf("%+v", r.Notes)
	}
	// A body that was read and one that was not are told apart by state, not by empty bytes.
	if got := byDetail(r); got[21].Body.State != BodyFile || got[21].Body.HTML == nil || got[22].Body.State != BodyNotRead || got[22].Body.HTML != nil {
		t.Fatalf("%+v %+v", got[21].Body, got[22].Body)
	}
}

func TestCollectCopiesWithTheSameStampTieByBlock(t *testing.T) {
	// Two header keys of one message carry the same stamp; the copy in the later block is shown.
	first := framed(append(folderObjs(), hdr(11, 21, fSent, 4, "earlier block", 2), det(21, "<a@example.invalid>"))...)
	second := framed(hdr(12, 21, fInbox, 4, "later block", 2))
	r := collect(t, storeOf(t, first, second), Options{})
	if r.Messages[0].Folder.Key != fInbox || r.Messages[0].Subject != "later block" || r.Messages[0].Copies != 2 {
		t.Fatalf("%+v", r.Messages[0])
	}
}

// A header reached after unknown bytes is not used, but when it maps it still names its message:
// the key is seen. One that does not map makes the read doubtful, and nothing else.
func TestCollectResyncedHeaderNamesItsKey(t *testing.T) {
	objs := append(folderObjs(), hdr(12, 22, fInbox, 1, "ok", 2), det(22, "<b@example.invalid>"))
	stray := append([]byte{1, 2, 3}, hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: 14, DetailKey: 23, FolderKey: fInbox}).Encode()...)
	r := collect(t, storeOf(t, append(framed(objs...), stray...)), Options{})
	if !slices.Equal(r.SeenDetailKeys, []uint32{22, 23}) || r.Doubtful || len(r.Messages) != 1 {
		t.Fatalf("%+v doubtful=%v", r.SeenDetailKeys, r.Doubtful)
	}
	old := mapHeader
	mapHeader = func(hxstore.Object) (Header, error) { return Header{}, errors.New("unmappable") }
	t.Cleanup(func() { mapHeader = old })
	r = collect(t, storeOf(t, append(framed(objs...), stray...)), Options{})
	if !r.Doubtful {
		t.Fatal("an unmappable resynced header did not make the read doubtful")
	}
}

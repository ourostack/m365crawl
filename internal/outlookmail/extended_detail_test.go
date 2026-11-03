package outlookmail

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// The measured 0x610 layout keeps the mapped words at their old offsets,
// but its string area begins at 1552 + lead, not 1551 + lead.
func extendedDetail(s hxbuild.MailDetailSpec) *hxbuild.Object {
	old := obj(hxbuild.NewMailDetail(s)).Raw
	o := hxbuild.NewObject(0xc9, 0x610, len(old)+1)
	for i := 8; i < 1551; i++ {
		o.PutU8(i, old[i])
	}
	o.PutU8(1551, 0xa5) // deliberately nonzero, with no mapped meaning
	for i := 1551; i < len(old); i++ {
		o.PutU8(i+1, old[i])
	}
	return o
}

func TestMapExtendedDetailUsesMeasuredStringBase(t *testing.T) {
	for _, lead := range []int{0, 1, 79} {
		t.Run(fmt.Sprint(lead), func(t *testing.T) {
			o := extendedDetail(hxbuild.MailDetailSpec{
				Key: 21, MessageID: "<detail@example.invalid>", Class: "IPM.Note",
				InReplyTo: "<parent@example.invalid>", Sent: at(2, 8), Lead: lead,
			})
			d, err := MapDetail(obj(o))
			if err != nil || d.Key != 21 || d.MessageID != "<detail@example.invalid>" || d.Class != "IPM.Note" ||
				d.InReplyTo != "<parent@example.invalid>" || !d.Sent.Equal(at(2, 8)) || d.BadStrings != 0 {
				t.Fatalf("extended detail: %+v %v", d, err)
			}
		})
	}
}

func TestMapExtendedDetailAbsentAndMalformedStrings(t *testing.T) {
	empty := extendedDetail(hxbuild.MailDetailSpec{Key: 21})
	d, err := MapDetail(obj(empty))
	if err != nil || d.MessageID != "" || d.Class != "" || d.InReplyTo != "" || !d.Sent.IsZero() || d.BadStrings != 0 {
		t.Fatalf("absent fields: %+v %v", d, err)
	}
	for _, length := range []uint32{3, 4 | 1<<31, 1<<30 | 1<<31} {
		for _, off := range []int{1216, 1232, 1240} {
			o := extendedDetail(hxbuild.MailDetailSpec{Key: 21, MessageID: "<a@example.invalid>", Class: "IPM.Note", InReplyTo: "<b@example.invalid>"})
			o.PutU32(off, length)
			d, err := MapDetail(obj(o))
			if err != nil || d.BadStrings != 1 || d.Key != 21 {
				t.Fatalf("malformed length at +%d: %+v %v", off, d, err)
			}
			if map[int]string{1216: d.InReplyTo, 1232: d.MessageID, 1240: d.Class}[off] != "" {
				t.Fatalf("malformed string at +%d was retained", off)
			}
		}
	}
	short := obj(empty)
	short.Raw = short.Raw[:1551]
	if _, err := MapDetail(short); err == nil {
		t.Fatal("extended detail shorter than its fixed region mapped")
	}
}

func TestMapDetailRefusesUnknownTag(t *testing.T) {
	for _, tag := range []uint16{0x60e, 0x611} {
		o := extendedDetail(hxbuild.MailDetailSpec{Key: 21})
		o.PutU16(2, tag)
		if _, err := MapDetail(obj(o)); err == nil {
			t.Fatalf("unmeasured tag 0x%x mapped", tag)
		}
	}
}

func TestCollectMixedDetailLayoutsSelectsLatestCopy(t *testing.T) {
	for _, extendedWins := range []bool{false, true} {
		t.Run(fmt.Sprint(extendedWins), func(t *testing.T) {
			oldStamp, extendedStamp := uint64(3), uint64(2)
			want := "<old@example.invalid>"
			if extendedWins {
				oldStamp, extendedStamp = extendedStamp, oldStamp
				want = "<extended@example.invalid>"
			}
			objs := append(folderObjs(),
				hdr(11, 21, fInbox, 1, "Fixture message", 2),
				det(22, "<parent@example.invalid>"),
				hdr(12, 22, fInbox, 1, "Fixture parent", 1),
				hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 21, Stamp: oldStamp, MessageID: "<old@example.invalid>", Class: "IPM.Note"}),
				extendedDetail(hxbuild.MailDetailSpec{Key: 21, Stamp: extendedStamp, MessageID: "<extended@example.invalid>", Class: "IPM.Note"}),
			)
			r := collect(t, storeOf(t, framed(objs...)), Options{})
			if len(r.Messages) != 2 || r.Messages[0].MessageID != want || len(r.Losses) != 0 || r.Doubtful {
				t.Fatalf("mixed detail selection: %+v %+v", r.Messages, r.Losses)
			}
		})
	}
}

func TestCollectUnknownExtendedDetailTagRefusesRead(t *testing.T) {
	o := extendedDetail(hxbuild.MailDetailSpec{Key: 21, MessageID: "<a@example.invalid>"})
	o.PutU16(2, 0x611)
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "Fixture message", 2), o)
	r, err := Collect(context.Background(), storeOf(t, framed(objs...)), t.TempDir(), "outlook/Test", Options{})
	var guard *hxstore.GuardError
	if !errors.As(err, &guard) || guard.Code != CodeMailLayoutUnsupported || len(r.Messages) != 0 {
		t.Fatalf("unknown detail did not refuse: %+v %v", r, err)
	}
}

func TestCollectMalformedExtendedDetailStringsAreBlankedAndCounted(t *testing.T) {
	o := extendedDetail(hxbuild.MailDetailSpec{Key: 21, MessageID: "<a@example.invalid>", Class: "IPM.Note", InReplyTo: "<b@example.invalid>"})
	o.PutU32(1232, 3) // odd UTF-16 length
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "Fixture message", 2), o)
	r := collect(t, storeOf(t, framed(objs...)), Options{})
	if len(r.Messages) != 1 || r.Messages[0].MessageID != "" || r.Messages[0].Class != "IPM.Note" ||
		r.Messages[0].InReplyTo != "<b@example.invalid>" || r.Notes.BadString != 1 || r.Notes.Unmapped != 0 {
		t.Fatalf("malformed extended string: %+v %+v", r.Messages, r.Notes)
	}
}

func TestCollectExtendedDetailResyncKeepsCleanCopy(t *testing.T) {
	clean := extendedDetail(hxbuild.MailDetailSpec{Key: 21, Stamp: 1, MessageID: "<clean@example.invalid>", Class: "IPM.Note"})
	stray := extendedDetail(hxbuild.MailDetailSpec{Key: 21, Stamp: 99, MessageID: "<stray@example.invalid>", Class: "IPM.Note"})
	objs := append(folderObjs(), hdr(11, 21, fInbox, 1, "Fixture message", 2), clean)
	payload := append(framed(objs...), append([]byte{1, 2, 3}, stray.Encode()...)...)
	r := collect(t, storeOf(t, payload), Options{})
	if len(r.Messages) != 1 || r.Messages[0].MessageID != "<clean@example.invalid>" || r.Notes.ResyncedKept != 1 || r.Doubtful {
		t.Fatalf("extended resync priority: %+v %+v", r.Messages, r.Notes)
	}
}

package transcripts

import (
	"fmt"
	"testing"
	"time"
)

func at(h, m int) time.Time { return time.Date(2026, 11, 3, h, m, 0, 0, time.UTC) }

func drivePart(call, item, msg string, starts, sentAt time.Time) Part {
	return Part{CallID: call, PartKey: "d:drive/" + item, MessageID: msg, StartsAt: starts, SentAt: sentAt, RefQuality: RefDriveItem,
		ContentTypes: "Recording+Transcript", DriveID: "drive", ItemID: item}
}

func brief(ps []Part) string {
	s := ""
	for _, p := range ps {
		s += fmt.Sprintf("%s#%d=%s/%s ", p.CallID, p.Ordinal, p.PartKey, p.MessageID)
	}
	return s
}

func TestAssembleOrdersByTimestamp(t *testing.T) {
	// The later part's notice arrived first.
	got := Assemble([]Part{
		drivePart("c", "late", "m1", at(10, 30), at(11, 0)),
		drivePart("c", "early", "m2", at(10, 0), at(11, 5)),
	}, nil)
	if brief(got) != "c#1=d:drive/early/m2 c#2=d:drive/late/m1 " {
		t.Fatalf("got %s", brief(got))
	}
}

func TestAssembleTieBreaks(t *testing.T) {
	same := at(10, 0)
	a := drivePart("c", "a", "m1", same, at(11, 0))
	a.OriginalName = "Weekly-20261103_100500-Meeting Recording.mp4"
	b := drivePart("c", "b", "m2", same, at(11, 1))
	b.OriginalName = "Weekly-20261103_100000-Meeting Recording.mp4"
	if got := brief(Assemble([]Part{a, b}, nil)); got != "c#1=d:drive/b/m2 c#2=d:drive/a/m1 " {
		t.Fatalf("name stamp: %s", got)
	}
	// No stamp in either name: the notice sent first comes first.
	a.OriginalName, b.OriginalName = "a.mp4", "b.mp4"
	if got := brief(Assemble([]Part{b, a}, nil)); got != "c#1=d:drive/a/m1 c#2=d:drive/b/m2 " {
		t.Fatalf("sent_at: %s", got)
	}
	// Everything equal: the message id decides, whatever the input order.
	b.SentAt = a.SentAt
	for _, in := range [][]Part{{a, b}, {b, a}} {
		if got := brief(Assemble(in, nil)); got != "c#1=d:drive/a/m1 c#2=d:drive/b/m2 " {
			t.Fatalf("message id: %s", got)
		}
	}
}

func TestAssembleDedupes(t *testing.T) {
	first := drivePart("c", "x", "m1", at(10, 0), at(11, 0))
	second := drivePart("c", "x", "m2", at(10, 0), at(12, 0))
	second.StorageKind = "newer"
	for _, in := range [][]Part{{first, second}, {second, first}} {
		got := Assemble(in, nil)
		if len(got) != 1 || got[0].MessageID != "m2" || got[0].StorageKind != "newer" || got[0].Ordinal != 1 {
			t.Fatalf("got %+v", got)
		}
	}
	// Two notices sent at the same instant: the larger message id wins, in either order.
	twin := drivePart("c", "x", "m9", at(10, 0), at(12, 0))
	for _, in := range [][]Part{{second, twin}, {twin, second}} {
		if got := Assemble(in, nil); len(got) != 1 || got[0].MessageID != "m9" {
			t.Fatalf("twin: %+v", got)
		}
	}
}

func TestAssembleUnresolvedCall(t *testing.T) {
	recOnly := drivePart("b", "x", "m1", at(9, 0), at(9, 30))
	recOnly.ContentTypes = "Recording"
	notices := map[string]Notice{
		"a": {ThreadID: "19:a@thread.v2", MessageID: "n-a", SentAt: at(8, 0)},   // no part at all
		"b": {ThreadID: "19:b@thread.v2", MessageID: "n-b", SentAt: at(9, 40)},  // a part with no transcript
		"c": {ThreadID: "19:c@thread.v2", MessageID: "n-c", SentAt: at(10, 40)}, // a part with a transcript
	}
	got := Assemble([]Part{recOnly, drivePart("c", "y", "m2", at(10, 0), at(10, 30))}, notices)
	if brief(got) != "a#1=call:a/n-a b#1=d:drive/x/m1 b#2=call:b/n-b c#1=d:drive/y/m2 " {
		t.Fatalf("got %s", brief(got))
	}
	u := got[0]
	if u.RefQuality != RefUnresolved || u.TranscribeOnly || u.ThreadID != "19:a@thread.v2" || !u.SentAt.Equal(at(8, 0)) || !u.StartsAt.IsZero() || u.Fetchable() {
		t.Fatalf("unresolved row %+v", u)
	}
}

func TestAssembleMultiDrive(t *testing.T) {
	one := drivePart("c", "x", "m1", at(10, 0), at(11, 0))
	two := drivePart("c", "x", "m2", at(10, 20), at(11, 1))
	two.DriveID, two.PartKey = "other", "d:other/x"
	got := Assemble([]Part{two, one}, nil)
	if brief(got) != "c#1=d:drive/x/m1 c#2=d:other/x/m2 " {
		t.Fatalf("got %s", brief(got))
	}
	if Assemble(nil, nil) != nil {
		t.Fatal("no notices, no rows")
	}
}

func TestAssembleProcessingToSuccess(t *testing.T) {
	ready := drivePart("c", "one", "ready", at(10, 0), at(11, 0))
	ready.ProcessingID = "one"
	pending := Part{CallID: "c", PartKey: "p:two", ProcessingID: "two", RefQuality: RefProcessing, StartsAt: at(10, 30), SentAt: at(11, 1)}
	finished := pending
	finished.MessageID, finished.SentAt, finished.ContentTypes = "finished", at(11, 2), "Recording+Transcript"
	got := Assemble([]Part{finished, ready, pending}, map[string]Notice{"c": {MessageID: "transcript"}})
	if len(got) != 2 || got[1].MessageID != "finished" || got[1].RefQuality != RefProcessing {
		t.Fatalf("pending recording: %+v", got)
	}
	success := drivePart("c", "two", "success", at(10, 30), at(11, 3))
	success.ProcessingID = "two"
	// The file name may change timezones; the processing id still binds the recording.
	success.OriginalName = "Meeting-20261103_023000.mp4"
	for _, parts := range [][]Part{{pending, success, finished, ready}, {ready, finished, success, pending}} {
		got = Assemble(parts, nil)
		if len(got) != 2 || got[1].PartKey != success.PartKey {
			t.Fatalf("resolved: %+v", got)
		}
	}
	failed := finished
	failed.RefQuality, failed.SentAt = RefRecordingFailed, at(11, 4)
	if got := Assemble([]Part{pending, failed}, nil); len(got) != 1 || got[0].RefQuality != RefRecordingFailed {
		t.Fatalf("failed notice: %+v", got)
	}
}

func TestAssembleProcessingFallbackIsScopedAndUnambiguous(t *testing.T) {
	pending := Part{CallID: "c", PartKey: "p:two", RefQuality: RefProcessing, OriginalName: "Meeting-20261103_103000UTC.mp4"}
	success := drivePart("c", "two", "success", at(10, 30), at(11, 3))
	for _, ids := range [][2]string{{"", ""}, {"two", ""}, {"", "two"}} {
		pending.ProcessingID, success.ProcessingID = ids[0], ids[1]
		if got := Assemble([]Part{pending, success}, nil); len(got) != 1 || got[0].PartKey != success.PartKey {
			t.Fatalf("fallback: %+v", got)
		}
	}
	pending.ProcessingID, success.ProcessingID = "different", "two"
	if got := Assemble([]Part{pending, success}, nil); len(got) != 2 {
		t.Fatalf("different processing ids merged: %+v", got)
	}
	pending.ProcessingID = ""
	other := success
	other.CallID = "other"
	if got := Assemble([]Part{pending, other}, nil); len(got) != 2 {
		t.Fatalf("different calls merged: %+v", got)
	}
	other = success
	other.PartKey, other.ProcessingID = "d:drive/other", "other"
	if got := Assemble([]Part{pending, success, other}, nil); len(got) != 3 {
		t.Fatalf("ambiguous start hid pending recording: %+v", got)
	}
}

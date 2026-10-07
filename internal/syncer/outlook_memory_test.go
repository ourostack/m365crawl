package syncer

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// mixRoot is an Outlook root whose store has the object mix of a real one at one eighth of its size
// (a real store is about 88 MiB and 263,000 objects): most objects are of classes no reader maps.
func mixRoot(t *testing.T) (root string, mix hxbuild.MixOptions) {
	t.Helper()
	mix = hxbuild.MixOptions{Events: 1075, EventIDs: 425, Details: 75, Messages: 1875, Recipients: 8500, Filler: 15000, FillerNoise: 700, BodyBytes: 2000, Codec: hxbuild.CodecMatches}
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Main"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), hxbuild.Mix(mix), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, mix
}

func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// The mail read starts with the heap the calendar read gave back (A19): once the calendar is
// committed nothing of it, and nothing of the store's objects, is still reachable.
func TestOutlookCalendarReleasedBeforeMail(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root, mix := mixRoot(t)
	var base, held uint64
	gcAtCalendar, gcAtCommit := 0, 0
	old := afterCalendar
	afterCalendar = func() { held = liveHeap() - base; gcAtCalendar = gcPercent() }
	t.Cleanup(func() { afterCalendar = old })
	oldNow := outlookNow
	outlookNow = func() time.Time {
		if gcAtCalendar != 0 { // the clock is read after the calendar commit, when the mail run record is written
			gcAtCommit = max(gcAtCommit, gcPercent())
		}
		return oldNow()
	}
	t.Cleanup(func() { outlookNow = oldNow })
	base = liveHeap()
	r, _ := run(t, outlookOpts(db, root))
	if mail := sourceKeyed(t, r, "outlook|Main|mail"); mail.Counts == nil || mail.Counts.Mail == nil || mail.Counts.Mail.Added != mix.Messages {
		t.Fatalf("mail row: %+v", mail)
	}
	// The garbage collector stays at the Outlook setting until the mail is committed, and is back
	// at the caller's setting after.
	if gcAtCalendar != outlookGCPercent || gcAtCommit != outlookGCPercent || gcPercent() != 100 {
		t.Fatalf("GC percent %d after the calendar, %d at the mail commit, %d after the sync; want %d, %d, 100", gcAtCalendar, gcAtCommit, gcPercent(), outlookGCPercent, outlookGCPercent)
	}
	t.Logf("reachable when the calendar is committed: %d B", held)
	if held > 1<<20 {
		t.Fatalf("%d B still reachable once the calendar is committed", held)
	}
}

// gcPercent reads the garbage collector's target.
func gcPercent() int {
	p := debug.SetGCPercent(100)
	debug.SetGCPercent(p)
	return p
}

// A whole Outlook sync of the mixed store allocates a bounded amount. Compressing each body with
// a new gzip writer once allocated over a megabyte a message: about 17 GB for a real mailbox,
// where the store file is 88 MB.
func TestOutlookSyncAllocations(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root, _ := mixRoot(t)
	info, err := os.Stat(filepath.Join(root, "Main", "HxStore.hxd"))
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	run(t, outlookOpts(db, root))
	runtime.ReadMemStats(&after)
	got := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d B allocated by a sync of a %d B store", got, info.Size())
	if got > uint64(info.Size())*30 { //nolint:gosec // a store file size is not negative
		t.Fatalf("%d B allocated, more than 30 times the %d B store file", got, info.Size())
	}
}

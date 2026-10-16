package syncer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func attendeeTestRoot(t *testing.T, mail bool) string {
	t.Helper()
	root := outlookRoot(t, "HxStore.hxd")
	if mail {
		root = mailRoot(t)
	}
	native := hxbuild.NewObject(0x71, 0xb8, 184)
	native.PutU32(20, 11)
	native.PutU32(32, 3)
	native.PutU64(112, 42)
	native.AppendString("Native")
	native.PutU32(156, 14)
	start := time.Date(2031, 3, 5, 9, 30, 0, 0, time.UTC)
	objects := []*hxbuild.Object{
		hxbuild.NewEvent(hxbuild.EventSpec{ID: hxbuild.GlobalObjectID(0, 0, 0, "attendee-fixture"), SeriesKey: 1, Stamp: 1, DetailKey: 3, Start: start, End: start.Add(time.Hour),
			LastModified: start.Add(-time.Hour), Subject: "Synthetic native", ZoneID: 2, ZoneName: "Fixture Standard Time"}),
		hxbuild.NewDetail(hxbuild.DetailSpec{Key: 3, SeriesKey: 1}), native,
	}
	if mail {
		objects = append(objects, mailObjects("~/Files/two.dat")...)
	}
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.FramedPayload(hxbuild.Head(15), objects...))
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

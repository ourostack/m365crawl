package outlookmail

import (
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func TestMapFolderKinds(t *testing.T) {
	for typ, want := range map[uint32]string{0x61: "inbox", 0x63: "archive", 0x64: "drafts", 0x65: "sent", 0x67: "deleted", 0x7a: "junk_or_to_me", 0x70: "other", 0: "other"} {
		f, err := MapFolder(obj(hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 5, Parent: 3, Name: "Fixture Folder", Type: typ, Lead: 79})))
		if err != nil || f.Key != 5 || f.Parent != 3 || f.Name != "Fixture Folder" || f.Kind != want {
			t.Errorf("type %#x: %+v %v", typ, f, err)
		}
	}
	f, err := MapFolder(obj(hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 6, Type: 0x61})))
	if err != nil || f.Name != "" {
		t.Fatalf("unnamed folder: %+v %v", f, err)
	}
	o := hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 6, Name: "Fixture Folder"})
	o.PutU32(1092, 4|1<<31)
	if _, err := MapFolder(obj(o)); err == nil || !strings.Contains(err.Error(), "string") {
		t.Fatal(err)
	}
	short := obj(hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 6}))
	short.Raw = short.Raw[:1000]
	if _, err := MapFolder(short); err == nil {
		t.Fatal("short object")
	}
}

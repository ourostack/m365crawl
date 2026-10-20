package outlookmail

import (
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func TestMapFolderKinds(t *testing.T) {
	for typ, want := range map[uint32]string{0x61: "inbox", 0x63: "archive", 0x64: "drafts", 0x65: "sent", 0x67: "deleted", 0x7a: "other", 0x70: "other", 0: "other"} {
		f, err := MapFolder(obj(hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 5, Parent: 3, Name: "Fixture Folder", Type: typ, Lead: 79})))
		if err != nil || f.Key != 5 || f.Parent != 3 || f.Name != "Fixture Folder" || f.Kind != want || f.Generic != (typ == 0x7a) {
			t.Errorf("type %#x: %+v %v", typ, f, err)
		}
	}
	f, err := MapFolder(obj(hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 6, Type: 0x61})))
	if err != nil || f.Name != "" {
		t.Fatalf("unnamed folder: %+v %v", f, err)
	}
	o := hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 6, Name: "Fixture Folder"})
	o.PutU32(1092, 4|1<<31)
	if f, err := MapFolder(obj(o)); err != nil || f.Name != "" || f.BadStrings != 1 || f.Key != 6 {
		t.Fatalf("%+v %v", f, err)
	}
	short := obj(hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: 6}))
	short.Raw = short.Raw[:1000]
	if _, err := MapFolder(short); err == nil {
		t.Fatal("short object")
	}
}

// The measured 0x4d8 layout moves the name/type words and string base by eight,
// but keeps the common key, parent, lead and stamp words.
func extendedFolder(s hxbuild.MailFolderSpec) *hxbuild.Object {
	o := hxbuild.NewObject(ClassFolder, 0x4d8, 1240)
	o.PutU32(20, s.Key)
	o.PutU32(40, s.Key)
	o.PutU32(32, s.Parent)
	o.PutU32(104, uint32(s.Lead))
	o.PutU64(112, s.Stamp)
	o.PutU32(1168, s.Type)
	o.Append(make([]byte, s.Lead))
	if s.Name != "" {
		o.PutStringWord(1096, 1240+s.Lead, s.Name)
		o.PutU32(1100, uint32(len(hxbuild.UTF16Z(s.Name)))|1<<31)
	}
	return o
}

func TestMapFolderExtendedLayout(t *testing.T) {
	for typ, want := range map[uint32]string{0x61: "inbox", 0x63: "archive", 0x64: "drafts", 0x65: "sent", 0x67: "deleted", 0x7a: "other", 0x70: "other", 0: "other"} {
		o := extendedFolder(hxbuild.MailFolderSpec{Key: 5, Parent: 3, Name: "Not an Inbox", Type: typ, Lead: 79})
		o.PutU32(1160, 0x65)
		f, err := MapFolder(obj(o))
		if err != nil || f.Key != 5 || f.Parent != 3 || f.Name != "Not an Inbox" || f.Kind != want || f.Generic != (typ == 0x7a) || f.BadStrings != 0 {
			t.Errorf("type %#x: %+v %v", typ, f, err)
		}
	}
	empty, err := MapFolder(obj(extendedFolder(hxbuild.MailFolderSpec{Key: 6, Type: 0x61})))
	if err != nil || empty.Name != "" || empty.Kind != "inbox" || empty.BadStrings != 0 {
		t.Fatalf("unnamed extended folder: %+v %v", empty, err)
	}
	bad := extendedFolder(hxbuild.MailFolderSpec{Key: 7, Name: "Fixture Folder"})
	bad.PutU32(1100, 4|1<<31)
	if f, err := MapFolder(obj(bad)); err != nil || f.Name != "" || f.BadStrings != 1 || f.Key != 7 {
		t.Fatalf("bad extended string: %+v %v", f, err)
	}
	short := obj(extendedFolder(hxbuild.MailFolderSpec{Key: 8}))
	short.Raw = short.Raw[:1232]
	if _, err := MapFolder(short); err == nil {
		t.Fatal("accepted an extended object shorter than its fixed region")
	}
}

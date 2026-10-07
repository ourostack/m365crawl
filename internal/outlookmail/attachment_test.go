package outlookmail

import (
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func TestMapAttachment(t *testing.T) {
	spec := hxbuild.AttachmentSpec{Key: 3, MessageKey: 21, Name: "fixture.png", Path: "~/Files/S0/2/Attachments/0/fixture[1].png", Size: 72, State: 2, Lead: 59}
	a, err := MapAttachment(obj(hxbuild.NewAttachment(spec)))
	want := Attachment{Key: 3, MessageKey: 21, Name: "fixture.png", Size: 72, ContentType: "image/png", Inline: false, Downloaded: true, Path: "~/Files/S0/2/Attachments/0/fixture[1].png"}
	if err != nil || a != want {
		t.Fatalf("%+v %v", a, err)
	}
	spec.State = 5
	if a, _ := MapAttachment(obj(hxbuild.NewAttachment(spec))); a.Downloaded {
		t.Fatal("state 5 is not downloaded")
	}
}

func TestAttachmentContentType(t *testing.T) {
	for name, want := range map[string]string{
		"a.png": "image/png", "a.JPG": "image/jpeg", "a.jpeg": "image/jpeg", "a.gif": "image/gif", "a.ics": "text/calendar",
		"a.pdf": "application/pdf", "a.txt": "text/plain", "a.zip": "application/zip",
		"a.docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"a.xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"a.pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"a.xyz":  "application/octet-stream", "noext": "application/octet-stream", "": "application/octet-stream",
	} {
		if got := contentType(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

func TestBareGUIDNameIsInline(t *testing.T) {
	g := "0123abcd-4567-89ef-0123-456789ABCDEF"
	for name, want := range map[string]bool{
		g: true, "{" + g: false, "{" + g + "}": true, "0123abcd456789ef0123456789ABCDEF": true,
		g + ".png": false, "fixture.png": false, "": false, "0123abcd-4567-89ef-0123-456789ABCDEG": false,
		"0123abcd456789ef0123456789ABCDE": false, "0123abcd-4567-89ef-0123-456789ABCDEFF": false, "0123abcd-4567-89ef-01234-56789ABCDEF": false,
		"{" + g + "": false, g + "}": false, "{": false, "{}": false,
	} {
		if got := bareGUID(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
	a, err := MapAttachment(obj(hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 1, MessageKey: 2, Name: g, Size: 5})))
	if err != nil || !a.Inline || a.ContentType != "application/octet-stream" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestMapAttachmentErrors(t *testing.T) {
	a, err := MapAttachment(obj(hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 1, MessageKey: 2})))
	if err != nil || a.Name != "" || a.Path != "" || a.Inline || a.ContentType != "application/octet-stream" {
		t.Fatalf("absent strings: %+v %v", a, err)
	}
	for _, word := range []int{612, 652} {
		o := hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 1, MessageKey: 2, Name: "fixture.txt", Path: "~/Files/x"})
		o.PutU32(word, 4|1<<31)
		if _, err := MapAttachment(obj(o)); err == nil {
			t.Errorf("+%d: bad string accepted", word)
		}
	}
	short := obj(hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 1}))
	short.Raw = short.Raw[:500]
	if _, err := MapAttachment(short); err == nil {
		t.Fatal("short object")
	}
}

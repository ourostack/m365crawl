package outlookcal

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/hxstore"
	"github.com/ourostack/teamscrawl/internal/hxstore/hxbuild"
)

func at(day, hour int) time.Time { return time.Date(2031, 3, day, hour, 0, 0, 0, time.UTC) }

// baseSpec is a complete invented event.
func baseSpec(n byte) hxbuild.EventSpec {
	return hxbuild.EventSpec{
		ID:           hxbuild.GlobalObjectID(0, 0, 0, "FIXTURE-TEST-00"+string('0'+n)),
		SeriesKey:    0xaa00 + uint64(n),
		DetailKey:    500 + uint32(n),
		LastModified: at(1, 8), Start: at(5, 9), End: at(5, 10),
		ZoneName: "Pacific Standard Time", ShowAs: 2, EventType: 0, Response: 0,
		Subject: "Fixture subject", SubjectBare: "Fixture subject", Location: "Fixture Room",
		OrganizerName: "Fixture Organizer", OrganizerAddr: "organizer@example.invalid", Preview: "Fixture preview",
		Attendees:   []hxbuild.Attendee{{Name: "Fixture One", Address: "one@example.invalid", A: 1, B: 0}, {Name: "Fixture Two", Address: "two@example.invalid", B: 4}},
		AreaOneSize: 812,
	}
}

func baseDetail(n byte) hxbuild.DetailSpec {
	return hxbuild.DetailSpec{
		Key: 500 + uint32(n), JoinLink: "https://example.invalid/fixture/join/1", DialIn: "Fixture dial-in",
		BodyHTML: "<p>Fixture body</p>", Lead: 3,
	}
}

// obj turns a built object into the reader's Object, the way Walk hands it over.
func obj(o *hxbuild.Object) hxstore.Object {
	raw := o.Encode()[4:]
	return hxstore.Object{Raw: raw, Class: binary.LittleEndian.Uint16(raw[10:]), Tag: binary.LittleEndian.Uint16(raw[2:])}
}

func ev(s hxbuild.EventSpec) hxstore.Object { return obj(hxbuild.NewEvent(s)) }
func det(s hxbuild.DetailSpec) *hxstore.Object {
	o := obj(hxbuild.NewDetail(s))
	return &o
}

// storeOf builds a store from payloads (one literal block each, framed with a 15-byte head)
// and opens it.
func storeOf(t *testing.T, payloads ...[]byte) *hxstore.Store {
	t.Helper()
	b := hxbuild.New(hxbuild.Options{})
	for _, p := range payloads {
		b.BlockCodec(p, hxbuild.CodecLiteral)
	}
	return open(t, b.Bytes())
}

func open(t *testing.T, data []byte) *hxstore.Store {
	t.Helper()
	s, err := OpenStore(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func framed(objs ...*hxbuild.Object) []byte { return hxbuild.FramedPayload(hxbuild.Head(15), objs...) }

func collect(t *testing.T, s *hxstore.Store, o Options) Result {
	t.Helper()
	r, err := Collect(context.Background(), s, "outlook/Test", o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func bytesReaderAt(b []byte) *bytes.Reader { return bytes.NewReader(b) }

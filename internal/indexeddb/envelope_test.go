package indexeddb

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/golang/snappy"
	"github.com/ourostack/teamscrawl/internal/v8"
)

// v8 "hi": version 15 header, one-byte string tag, length 2.
var v8Hi = []byte{0xff, 0x0f, 0x22, 0x02, 'h', 'i'}

func omissionCode(t *testing.T, err error) string {
	t.Helper()
	var oe *OmissionError
	if !errors.As(err, &oe) {
		t.Fatalf("want *OmissionError, got %T %v", err, err)
	}
	return oe.Code
}

func TestEnvelopePlain(t *testing.T) {
	o := newTestOrigin(fakeKV{}, "")
	raw := append([]byte{0xff, 0x10}, v8Hi...)
	got, err := o.Decode(1, raw)
	if err != nil || got != "hi" {
		t.Fatalf("got %#v %v", got, err)
	}
	if k := EnvelopeKind(raw); k != "plain" {
		t.Errorf("kind %q", k)
	}
}

func TestEnvelopeSnappy(t *testing.T) {
	o := newTestOrigin(fakeKV{}, "")
	inner := append([]byte{0xff, 0x10}, v8Hi...)
	raw := append([]byte{0xff, 0x11, 0x02}, snappy.Encode(nil, inner)...)
	got, err := o.Decode(1, raw)
	if err != nil || got != "hi" {
		t.Fatalf("got %#v %v", got, err)
	}
	if k := EnvelopeKind(raw); k != "snappy" {
		t.Errorf("kind %q", k)
	}
	bad := []byte{0xff, 0x11, 0x02, 0xff, 0xff, 0xff}
	if _, err := o.Decode(1, bad); omissionCode(t, err) != "unknown_envelope" {
		t.Errorf("bad snappy: %v", err)
	}
}

func v21Envelope(payload, trailerBody []byte) []byte {
	out := []byte{0xff, 0x15, 0xfe}
	off := uint64(15 + len(payload))
	var b [12]byte
	binary.BigEndian.PutUint64(b[:8], off)
	binary.BigEndian.PutUint32(b[8:], uint32(len(trailerBody))) //nolint:gosec // bounded by earlier length checks
	out = append(out, b[:]...)
	out = append(out, payload...)
	return append(out, trailerBody...)
}

func TestEnvelopeV21Trailer(t *testing.T) {
	o := newTestOrigin(fakeKV{}, "")
	raw := v21Envelope(v8Hi, []byte{0xa0, 0x01, 0x02})
	got, err := o.Decode(1, raw)
	if err != nil || got != "hi" {
		t.Fatalf("got %#v %v", got, err)
	}
	if k := EnvelopeKind(raw); k != "v21" {
		t.Errorf("kind %q", k)
	}
	// Trailer claims to extend past the end of the data.
	broken := v21Envelope(v8Hi, []byte{1, 2, 3})
	broken = broken[:len(broken)-2]
	if _, err := o.Decode(1, broken); omissionCode(t, err) != "unknown_envelope" {
		t.Errorf("overrun: %v", err)
	}
	// Missing trailer tag.
	if _, err := o.Decode(1, []byte{0xff, 0x15, 0x00, 0, 0}); omissionCode(t, err) != "unknown_envelope" {
		t.Errorf("no tag: %v", err)
	}
}

func blobRefValue(size, index uint64) []byte {
	out := []byte{0xff, 0x11, 0x01}
	out = append(out, varint(size)...)
	return append(out, varint(index)...)
}

func TestEnvelopeBlobReplace(t *testing.T) {
	dir := t.TempDir()
	const dbID, n = 0x1a, 0x1234
	p := filepath.Join(dir, "1a", "12")
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	inner := v21Envelope(v8Hi, nil)
	if err := os.WriteFile(filepath.Join(p, "1234"), inner, 0o600); err != nil {
		t.Fatal(err)
	}
	o := newTestOrigin(fakeKV{}, dir)
	// Decode takes the blob number directly (Records resolves the index).
	raw := blobRefValue(uint64(len(inner)), n)
	got, err := o.Decode(dbID, raw)
	if err != nil || got != "hi" {
		t.Fatalf("got %#v %v", got, err)
	}
	if k := EnvelopeKind(raw); k != "blob" {
		t.Errorf("kind %q", k)
	}
}

func TestBlobMissing(t *testing.T) {
	o := newTestOrigin(fakeKV{}, t.TempDir())
	if _, err := o.Decode(1, blobRefValue(10, 5)); omissionCode(t, err) != "blob_missing" {
		t.Errorf("missing file: %v", err)
	}
	o = newTestOrigin(fakeKV{}, "")
	if _, err := o.Decode(1, blobRefValue(10, 5)); omissionCode(t, err) != "blob_missing" {
		t.Errorf("no blob dir: %v", err)
	}
}

func TestUnknownEnvelope(t *testing.T) {
	o := newTestOrigin(fakeKV{}, "")
	for _, raw := range [][]byte{
		{0x00, 0x01},             // no Blink tag
		{0xff},                   // header cut off
		{0xff, 0x11, 0x07},       // unknown wrapper kind
		{0xff, 0x11},             // wrapper kind missing
		{0xff, 0x11, 0x01, 0x05}, // blob ref truncated
		{0xff, 0x00, 0xff, 0x0f}, // version 0
	} {
		_, err := o.Decode(1, raw)
		if omissionCode(t, err) != "unknown_envelope" {
			t.Errorf("% x: %v", raw, err)
		}
		if k := EnvelopeKind(raw); k != "unknown" && k != "blob" {
			t.Errorf("% x: kind %q", raw, k)
		}
	}
	if k := EnvelopeKind([]byte{0x00}); k != "unknown" {
		t.Errorf("kind %q", k)
	}
}

func TestEmptyValue(t *testing.T) {
	o := newTestOrigin(fakeKV{}, "")
	for _, raw := range [][]byte{nil, {}} {
		_, err := o.Decode(1, raw)
		if omissionCode(t, err) != "empty_value" {
			t.Errorf("got %v", err)
		}
	}
	if k := EnvelopeKind(nil); k != "unknown" {
		t.Errorf("kind %q", k)
	}
}

func TestV8ErrorsPassThrough(t *testing.T) {
	o := newTestOrigin(fakeKV{}, "")
	// 0x5c ('\\') is not a known V8 tag; the v8 package reports v8_unknown_tag.
	raw := append([]byte{0xff, 0x10}, 0xff, 0x0f, 0x5c)
	_, err := o.Decode(1, raw)
	var ue *v8.UnsupportedError
	if _, probe := v8.Deserialize([]byte{0xff, 0x0f, 0x5c}); !errors.As(probe, &ue) {
		t.Skip("probe byte is not an unsupported tag in this v8 build")
	}
	if got := omissionCode(t, err); got != ue.Code {
		t.Errorf("code %q, want %q", got, ue.Code)
	}
	// A version error gets its own code.
	_, err = o.Decode(1, []byte{0xff, 0x10, 0xff, 0x63})
	if got := omissionCode(t, err); got != "v8_version" {
		t.Errorf("version: %q", got)
	}
	// Plain truncation gets v8_malformed.
	_, err = o.Decode(1, []byte{0xff, 0x10, 0xff, 0x0f, 0x22, 0x09, 'a'})
	if got := omissionCode(t, err); got != "v8_malformed" {
		t.Errorf("malformed: %q", got)
	}
}

func TestRecordsResolveBlobIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "5", "01"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "5", "01", "1ff"), v21Envelope(v8Hi, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	strKey := append([]byte{1}, idbString("k1")...)
	pfx1, _ := makePrefix(5, 2, 1)
	pfx3, _ := makePrefix(5, 2, 3)
	// external object list: one Blob (type 0), number 0x1ff, mime "", size 7.
	ext := append([]byte{0}, varint(0x1ff)...)
	ext = append(ext, varint(0)...)
	ext = append(ext, varint(7)...)
	f := fakeKV{
		string(append(append([]byte{}, pfx1...), strKey...)): append([]byte{0x00}, blobRefValue(7, 0)...),
		string(append(append([]byte{}, pfx3...), strKey...)): ext,
	}
	k2 := append([]byte{1}, idbString("k2")...)
	f[string(append(append([]byte{}, pfx1...), k2...))] = append([]byte{0x00}, blobRefValue(7, 4)...) // no blob info
	k3 := append([]byte{1}, idbString("k3")...)
	f[string(append(append([]byte{}, pfx1...), k3...))] = nil // empty value
	o := newTestOrigin(f, dir)
	var keys []any
	var decoded []any
	var codes []string
	err := o.Records(5, 2, func(r Record) error {
		keys = append(keys, r.Key)
		v, err := o.Decode(5, r.Raw)
		decoded = append(decoded, v)
		if err != nil {
			codes = append(codes, omissionCode(t, err))
		} else {
			codes = append(codes, "")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []any{"k1", "k2", "k3"}) {
		t.Fatalf("keys %v", keys)
	}
	if !reflect.DeepEqual(codes, []string{"", "blob_missing", "empty_value"}) || decoded[0] != "hi" {
		t.Fatalf("codes %v decoded %v", codes, decoded)
	}
	if o.Stats().Keys != len(f) {
		t.Errorf("stats %+v", o.Stats())
	}
}

func TestEnvelopeKindValues(t *testing.T) {
	for raw, want := range map[string]string{
		"\xff\x10\xff\x0f":     "plain",
		"\xff\x11\x02abc":      "snappy",
		"\xff\x11\x01\x01\x01": "blob",
		"\xff\x15\xfe":         "v21",
		"\xff\x15\x00":         "unknown",
		"\xff\x11\x09":         "unknown",
		"":                     "unknown",
		"hello":                "unknown",
	} {
		if got := EnvelopeKind([]byte(raw)); got != want {
			t.Errorf("%q: got %s want %s", raw, got, want)
		}
	}
}

package outlookattendees

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func sourceBytes(t *testing.T, raw []byte) *hxstore.Store {
	t.Helper()
	s, err := hxstore.Open(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCollectResyncedEvidenceAndConflicts(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.FramedPayload(nil, attendeeObject(1, 2, nil, nil)))
	b.Block(append([]byte{0x42}, hxbuild.Payload(attendeeObject(1, 2, nil, nil))...))
	b.Block(append([]byte{0x42}, hxbuild.Payload(detail(2))...))
	b.Block(append([]byte{0x42}, hxbuild.Payload(event(3, 2))...))
	got, err := Collect(context.Background(), sourceBytes(t, b.Bytes()))
	if err != nil || len(got.Attendees) != 2 || got.Attendees[0].Resynced || !got.Attendees[1].Resynced ||
		!reflect.DeepEqual(got.AmbiguousKeys, []uint32{1}) ||
		!reflect.DeepEqual(got.Losses, []Loss{{"outlook_attendees_geometry_resynced", 2}, {"outlook_attendees_resynced", 1}}) {
		t.Fatalf("resynchronized provenance concealed: %+v,%v", got, err)
	}
}

func TestCollectDamageAndPoorCoverage(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.BlockType(9, []byte{0, 0, 0, 0})
	b.Block(hxbuild.FramedPayload(nil, detail(2), event(3, 2), attendeeObject(1, 2, nil, nil)))
	b.Block(hxbuild.FramedPayload(nil, attendeeObject(4, 2, nil, nil)))
	raw := b.Bytes()
	raw[len(raw)-1] ^= 1
	got, err := Collect(context.Background(), sourceBytes(t, raw))
	if err != nil || len(got.Attendees) != 1 || !reflect.DeepEqual(got.Losses, []Loss{{"outlook_blocks_damaged", 1}}) {
		t.Fatalf("damage aggregation or other-type exclusion failed: %+v,%v", got, err)
	}
	b = hxbuild.New(hxbuild.Options{})
	b.Block(append(hxbuild.Payload(attendeeObject(1, 2, nil, nil)), make([]byte, 2000)...))
	got, err = Collect(context.Background(), sourceBytes(t, b.Bytes()))
	var guard *Error
	if !errors.As(err, &guard) || guard.Code != "outlook_attendees_layout_unsupported" || len(got.Attendees) != 0 {
		t.Fatalf("poor source framing published: %+v,%v", got, err)
	}
}

type cancellationCheckpoints struct {
	context.Context
	calls, cancelAt int
}

func (c *cancellationCheckpoints) Err() error {
	c.calls++
	if c.cancelAt > 0 && c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestCollectEveryCancellationCheckpoint(t *testing.T) {
	makeSource := func() *hxstore.Store {
		return source(t, detail(2), event(3, 2), attendeeObject(1, 2, text("N"), nil))
	}
	baseline := &cancellationCheckpoints{Context: context.Background()}
	if _, err := Collect(baseline, makeSource()); err != nil {
		t.Fatal(err)
	}
	for at := 1; at <= baseline.calls; at++ {
		ctx := &cancellationCheckpoints{Context: context.Background(), cancelAt: at}
		got, err := Collect(ctx, makeSource())
		if !errors.Is(err, context.Canceled) || len(got.Attendees) != 0 || len(got.Evidence) != 0 ||
			len(got.Losses) != 0 || len(got.AmbiguousKeys) != 0 {
			t.Fatalf("checkpoint %d published or hid cancellation: %+v,%v", at, got, err)
		}
	}
}

func TestReadErrorsRemainSafe(t *testing.T) {
	for _, cause := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		&Error{Code: "unexpected-private-value"},
		errors.New("private-value"),
	} {
		got := safeReadError(context.Background(), cause)
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			if !errors.Is(got, cause) {
				t.Fatalf("context identity changed: %v", got)
			}
		} else if got.Error() != "outlook_attendees_read_failed" || !errors.Is(got, cause) {
			t.Fatalf("arbitrary source error rendered or lost: %v", got)
		}
	}
}

type faultAfterWindow struct {
	raw *bytes.Reader
	err error
}

func (r faultAfterWindow) ReadAt(p []byte, off int64) (int, error) {
	if off > 1<<20 {
		return 0, r.err
	}
	return r.raw.ReadAt(p, off)
}

func TestCollectReadFailureAfterValidMappedObjects(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.FramedPayload(nil, detail(2), event(3, 2), attendeeObject(1, 2, nil, nil)))
	b.Pad(2 << 20)
	raw := b.Bytes()
	cause := errors.New("private-reader-value")
	s, err := hxstore.Open(faultAfterWindow{raw: bytes.NewReader(raw), err: cause}, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Collect(context.Background(), s)
	if err == nil || err.Error() != "outlook_attendees_read_failed" || !errors.Is(err, cause) ||
		len(got.Attendees) != 0 || len(got.Evidence) != 0 || got.Stats.Objects < 3 {
		t.Fatalf("failed source published observations or lost safe diagnostics: %+v,%v", got, err)
	}
}

package outlookmeetingattendees

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

func sample() (outlookattendees.Result, outlookcal.AttendeeSelections) {
	name, email := "Literal\x00\n<&>", ""
	return outlookattendees.Result{
		Attendees: []outlookattendees.Attendee{
			{Key: 9, DetailKey: 3, VersionRaw: math.MaxUint64, BlockOffset: math.MaxInt64, PayloadPos: 7, Name: &name, Email: &email},
			{Key: 9, DetailKey: 3, VersionRaw: math.MaxUint64, BlockOffset: 1, PayloadPos: 9, Name: &name, Email: &email},
		},
		Evidence: []outlookattendees.Evidence{{DetailKey: 3, DetailCopies: 1, EventLinks: 1}},
	}, outlookcal.AttendeeSelections{Rows: []outlookcal.SelectionReceipt{
		{SourceID: "synthetic-event", LastModifiedRaw: math.MaxUint64, StampRaw: math.MaxUint64, BlockOffset: math.MaxInt64, PayloadPos: 7, DetailKey: 3, DetailCopies: 1, DetailPresent: true},
	}}
}

func TestMeetingAttendeeEvidenceContract(t *testing.T) {
	ctx := context.Background()
	native, selections := sample()
	e, _, err := Build(ctx, native, selections)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte(`{"format":"outlook-attendee-evidence-v1","selections":[{"native_event_id":`)) ||
		!bytes.Contains(raw, []byte(`"version_raw":"18446744073709551615","block_offset":"9223372036854775807"`)) ||
		!bytes.Contains(raw, []byte(`"ambiguous_keys":[]`)) {
		t.Fatalf("canonical transport changed: %.120s", raw)
	}
	got, err := Decode(ctx, raw)
	if err != nil || !reflect.DeepEqual(e, got) {
		t.Fatalf("physical order/null/literal roundtrip failed: %v", err)
	}
	calls := 0
	summary, err := Audit(ctx, raw, func(i int, a outlookattendees.Attendee) error {
		if i != calls || a.Key != 9 || a.Name == nil || *a.Name != "Literal\x00\n<&>" {
			t.Fatal("visitor lost literal/order")
		}
		calls++
		return nil
	})
	if err != nil || calls != 2 || summary.AttendeesTotal != 2 || len(summary.AmbiguousKeys) != 0 {
		t.Fatalf("body-free audit lost counts: %+v %v", summary, err)
	}
	*e.Attendees[0].Name = "different"
	if *native.Attendees[0].Name != "Literal\x00\n<&>" {
		t.Fatal("owned evidence borrowed producer text")
	}
	bad := append([]byte(nil), raw...)
	for _, altered := range [][]byte{
		append(bad, '\n'),
		bytes.Replace(raw, []byte(`"format":`), []byte(`"unknown":`), 1),
		bytes.Replace(raw, []byte(`"native_key":9`), []byte(`"native_key":9,"native_key":9`), 1),
		bytes.Replace(raw, []byte(`18446744073709551615`), []byte(`018446744073709551615`), 1),
		bytes.Replace(raw, []byte(`"ambiguous_keys":[]`), []byte(`"ambiguous_keys":[9]`), 1),
		bytes.Replace(raw, []byte(`"losses":[]`), []byte(`"losses":[{"code":"outlook_attendees_detail_missing","count":"1"}]`), 1),
	} {
		if _, err := Decode(ctx, altered); err == nil {
			t.Fatal("noncanonical/contradictory evidence accepted")
		}
	}
	selections.Err = errors.New("synthetic refusal")
	if got, _, err := Build(ctx, native, selections); err == nil || len(got.Attendees) != 0 {
		t.Fatal("failed selection published candidate")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Decode(cancelled, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled audit succeeded: %v", err)
	}
}

func TestMeetingAttendeeEnvelopeBound(t *testing.T) {
	if err := checkEnvelopeSize(402653184, 402653184); err != nil {
		t.Fatal("exact literal envelope cap refused")
	}
	if err := checkEnvelopeSize(402653185, 402653184); err == nil {
		t.Fatal("literal envelope cap+1 accepted")
	}
	native, selected := sample()
	e, _, err := Build(context.Background(), native, selected)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auditBounded(context.Background(), raw, nil, len(raw)); err != nil {
		t.Fatal("real exact lower limit refused")
	}
	if _, err := auditBounded(context.Background(), raw, nil, len(raw)-1); err == nil {
		t.Fatal("real lower limit+1 accepted")
	}
	if _, err := Decode(context.Background(), []byte(strings.Repeat(" ", 50))); err == nil {
		t.Fatal("invalid bytes admitted")
	}
}

func TestMeetingAttendeeCensusRoundtrip(t *testing.T) {
	native, selected := sample()
	native.Stats = hxstore.Stats{BlocksFound: 1, BlocksValid: 1, Objects: 3, ObjectsResynced: 2,
		Pairs:             map[hxstore.Pair]int{{Class: 0xffff, Tag: 0xffff}: 3},
		PairsResynced:     map[hxstore.Pair]int{{Class: 0xffff, Tag: 0xffff}: 2},
		PairsResyncedLong: map[hxstore.Pair]int{{Class: 0xffff, Tag: 0xffff}: 1},
		HeadLens:          map[int]int{64: 1}, GapLens: map[int]int{0: 1}, TrailerHeadLens: map[int]int{4097: 1},
		Gap1Values: map[int]int{255: 1}, Gap11OneByteDiff: map[int]int{10: 1},
		NoObjectFirst4: map[string]int{"large": 1}, HeadFirst4: map[string]int{"zero": 1}, GapFirst4: map[string]int{"small": 1}}
	_, c, err := Build(context.Background(), native, selected)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeCensus(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"blocks_found":"1"`)) || !bytes.Contains(raw, []byte(`"class":65535,"tag":65535,"count":"3"`)) {
		t.Fatal("census decimal/pair transport lost")
	}
	got, err := DecodeCensus(context.Background(), raw)
	if err != nil || !reflect.DeepEqual(c, got) {
		t.Fatalf("census changed: %v", err)
	}
	c.Pairs = append(c.Pairs, c.Pairs[0])
	if _, err := EncodeCensus(context.Background(), c); err == nil {
		t.Fatal("duplicate census pair accepted")
	}
}

func TestMeetingAttendeeLiteralAndWriterRefusals(t *testing.T) {
	ctx := context.Background()
	native, selected := sample()
	e, _, err := Build(ctx, native, selected)
	if err != nil {
		t.Fatal(err)
	}
	invalid := string([]byte{0xff})
	e.Attendees = e.Attendees[:1]
	e.Attendees[0].Name = &invalid
	if _, err := Encode(ctx, e); err == nil {
		t.Error("invalid UTF8 silently normalized")
	}
	if err := writeEvidence(ctx, shortWriter{}, Evidence{}); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("short writer reported successful encoding: %v", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 0, nil }

func TestMeetingAttendeeSelectionRelationships(t *testing.T) {
	for _, change := range []func(*outlookattendees.Result, *outlookcal.AttendeeSelections){
		func(_ *outlookattendees.Result, s *outlookcal.AttendeeSelections) { s.Rows[0].DetailPresent = false },
		func(n *outlookattendees.Result, _ *outlookcal.AttendeeSelections) { n.Evidence[0].DetailCopies = 2 },
		func(_ *outlookattendees.Result, s *outlookcal.AttendeeSelections) { s.Rows = append(s.Rows, s.Rows[0]) },
	} {
		n, s := sample()
		change(&n, &s)
		if _, _, err := Build(context.Background(), n, s); err == nil {
			t.Error("contradictory/duplicated actual selection receipt admitted")
		}
	}
}

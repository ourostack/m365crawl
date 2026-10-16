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

type countWriter struct{ bytes int64 }

func (w *countWriter) Write(p []byte) (int, error) { w.bytes += int64(len(p)); return len(p), nil }

func TestMeetingAttendeeEnvelopeActualEncoderProof(t *testing.T) {
	// These are the reviewed inclusive component bounds, not a memory benchmark.
	e := Evidence{Attendees: make([]outlookattendees.Attendee, 262144), Selections: make([]outlookcal.SelectionReceipt, 65536),
		AmbiguousKeys: make([]uint32, 262144), Parents: make([]outlookattendees.Evidence, 65536)}
	text := strings.Repeat("\x00", 64)
	for i := range e.Attendees {
		e.Attendees[i] = outlookattendees.Attendee{Key: math.MaxUint32, DetailKey: math.MaxUint32,
			VersionRaw: math.MaxUint64, BlockOffset: math.MinInt64, PayloadPos: math.MaxInt, Name: &text}
		e.AmbiguousKeys[i] = math.MaxUint32
	}
	for i := range e.Selections {
		e.Selections[i] = outlookcal.SelectionReceipt{SourceID: strings.Repeat("\x00", 128),
			LastModifiedRaw: math.MaxUint64, StampRaw: math.MaxUint64, BlockOffset: math.MinInt64,
			PayloadPos: math.MaxInt, DetailKey: math.MaxUint32, DetailCopies: 524288, DetailResyncedCopies: 524288}
		e.Parents[i] = outlookattendees.Evidence{DetailKey: math.MaxUint32, DetailCopies: 524288, EventLinks: 524288}
	}
	// Encoder width proof deliberately maximizes each independent field; the
	// result need not claim that repeated maximal keys are an admissible model.
	var w countWriter
	if err := writeEvidence(context.Background(), &w, e); err != nil {
		t.Fatal(err)
	}
	if w.bytes+4194304+4194304+6*1048576 > 278921216 {
		t.Fatalf("actual encoder exceeds reviewed 266MiB derived envelope: %d", w.bytes)
	}
}

func TestMeetingAttendeeCensusEveryFieldAndMaximum(t *testing.T) {
	var stats hxstore.Stats
	value := reflect.ValueOf(&stats).Elem()
	for i := range value.NumField() {
		f := value.Field(i)
		switch f.Kind() {
		case reflect.Int, reflect.Int64:
			f.SetInt(math.MaxInt64)
		case reflect.Map:
			f.Set(reflect.MakeMap(f.Type()))
			for j := 0; j < 2; j++ {
				var key reflect.Value
				switch f.Type().Key().Kind() {
				case reflect.Int:
					key = reflect.ValueOf(j)
				case reflect.String:
					keys := []string{"large", "zero"}
					if value.Type().Field(i).Name == "Rejected" {
						keys = []string{"header_crc", "inflate"}
					}
					key = reflect.ValueOf(keys[j])
				default:
					key = reflect.ValueOf(hxstore.Pair{Class: uint16(j), Tag: uint16(j)})
				}
				f.SetMapIndex(key, reflect.ValueOf(math.MaxInt))
			}
		}
	}
	c := censusOf(stats)
	raw, err := EncodeCensus(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"9223372036854775807"`)) {
		t.Fatal("maximum census count lost decimal precision")
	}
	got, err := DecodeCensus(context.Background(), raw)
	if err != nil || !reflect.DeepEqual(c, got) {
		t.Fatalf("complete Stats census changed: %v", err)
	}
	for i := range reflect.TypeFor[hxstore.Stats]().NumField() {
		name := reflect.TypeFor[hxstore.Stats]().Field(i).Name
		if !reflect.ValueOf(c).FieldByName(name).IsValid() {
			t.Fatalf("Stats field omitted from owned model: %s", name)
		}
	}
	maximum := Census{}
	for i := range 4096 {
		row := PairCount{Class: uint16(i), Tag: math.MaxUint16, Count: math.MaxInt64}
		maximum.Pairs = append(maximum.Pairs, row)
		maximum.PairsResynced = append(maximum.PairsResynced, row)
		maximum.PairsResyncedLong = append(maximum.PairsResyncedLong, row)
	}
	for i := range 4098 {
		row := IntCount{Key: int64(i), Count: math.MaxInt64}
		maximum.TrailerHeadLens = append(maximum.TrailerHeadLens, row)
		if i <= 64 {
			maximum.HeadLens = append(maximum.HeadLens, row)
			maximum.GapLens = append(maximum.GapLens, row)
		}
		if i <= 255 {
			maximum.Gap1Values = append(maximum.Gap1Values, row)
		}
		if i <= 10 {
			maximum.Gap11OneByteDiff = append(maximum.Gap11OneByteDiff, row)
		}
	}
	for _, key := range []string{"large", "medium", "small", "zero"} {
		row := EnumCount{Key: key, Count: math.MaxInt64}
		maximum.NoObjectFirst4 = append(maximum.NoObjectFirst4, row)
		maximum.HeadFirst4 = append(maximum.HeadFirst4, row)
		maximum.GapFirst4 = append(maximum.GapFirst4, row)
	}
	for _, key := range []string{"header_crc", "header_unknown", "inflate", "magic", "oversize", "payload_crc", "truncated", "type_other"} {
		maximum.Rejected = append(maximum.Rejected, EnumCount{Key: key, Count: math.MaxInt64})
	}
	raw, err = EncodeCensus(context.Background(), maximum)
	if err != nil || len(raw) > 2216320 {
		t.Fatalf("16803-entry actual census encoder exceeds reviewed component: %d %v", len(raw), err)
	}
	maximum.Pairs = append(maximum.Pairs, PairCount{Class: 4096, Count: 1})
	if _, err := EncodeCensus(context.Background(), maximum); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("pair cap+1 accepted: %v", err)
	}
}

func TestMeetingAttendeeCanonicalFaultsAndCancellation(t *testing.T) {
	native, selections := sample()
	e, _, err := Build(context.Background(), native, selections)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	for i := range len(raw) {
		if _, err := Audit(context.Background(), raw[:i], nil); err == nil {
			t.Fatalf("truncated canonical model accepted at byte %d", i)
		}
	}
	stop := errors.New("synthetic visitor failure")
	if summary, err := Audit(context.Background(), raw, func(int, outlookattendees.Attendee) error { return stop }); !errors.Is(err, stop) || summary.AttendeesTotal != 0 {
		t.Fatal("visitor failure published staged summary")
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = Audit(ctx, raw, func(int, outlookattendees.Attendee) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal("visitor cancellation succeeded")
	}
	for _, change := range []func(*Evidence){
		func(e *Evidence) { e.Selections[0].SourceID = "" },
		func(e *Evidence) { e.Selections[0].DetailCopies = -1 },
		func(e *Evidence) { e.Selections[0].DetailDiffering = true },
		func(e *Evidence) { e.Attendees[0].Key = 0 },
		func(e *Evidence) { e.Attendees[0].DetailKey = 0 },
		func(e *Evidence) { e.Attendees[0].PayloadPos = -1 },
		func(e *Evidence) { e.Attendees[0].VersionRaw = 1 },
		func(e *Evidence) { e.Parents = nil },
		func(e *Evidence) { e.Parents[0].DetailCopies = -1 },
		func(e *Evidence) { e.Parents = append(e.Parents, e.Parents[0]) },
		func(e *Evidence) { e.Losses = []outlookattendees.Loss{{Code: "private", Count: 1}} },
		func(e *Evidence) { e.Parents[0].DetailCopies = 0 },
	} {
		native, selections := sample()
		candidate, _, err := Build(context.Background(), native, selections)
		if err != nil {
			t.Fatal(err)
		}
		change(&candidate)
		if _, err := Encode(context.Background(), candidate); err == nil {
			t.Fatal("contradictory canonical model admitted")
		}
	}
	// All existing native fixed losses and own-snapshot ambiguity survive.
	native.Attendees[0].Name = nil
	native.AmbiguousKeys = []uint32{9}
	native.Evidence[0].DetailCopies, native.Evidence[0].EventLinks = 0, 0
	native.Losses = []outlookattendees.Loss{}
	for _, code := range []string{"outlook_attendees_detail_missing", "outlook_attendees_event_link_missing", "outlook_attendees_geometry_resynced", "outlook_attendees_geometry_unmapped", "outlook_attendees_resynced", "outlook_attendees_unmapped", "outlook_blocks_damaged"} {
		native.Losses = append(native.Losses, outlookattendees.Loss{Code: code, Count: 1})
	}
	selections.Rows = nil
	withLoss, _, err := Build(context.Background(), native, selections)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = Encode(context.Background(), withLoss)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(context.Background(), raw)
	if err != nil || !reflect.DeepEqual(got.Losses, native.Losses) || !reflect.DeepEqual(got.AmbiguousKeys, []uint32{9}) {
		t.Fatal("complete loss/ambiguity model changed")
	}
	for i := 0; i <= 30; i++ {
		err := writeEvidence(context.Background(), &faultWriter{after: i}, withLoss)
		if err != nil && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	}
}

type faultWriter struct{ after int }

func (w *faultWriter) Write(p []byte) (int, error) {
	w.after--
	if w.after < 0 {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func TestMeetingAttendeeCensusFaults(t *testing.T) {
	ctx := context.Background()
	for _, c := range []Census{
		{BlocksFound: -1},
		{Pairs: []PairCount{{Count: 0}}},
		{PairsResynced: []PairCount{{Count: 1}}},
		{HeadLens: []IntCount{{Key: 65, Count: 1}}},
		{GapLens: make([]IntCount, 66)},
		{Rejected: make([]EnumCount, 9)},
		{Rejected: []EnumCount{{Key: "private", Count: 1}}},
		{HeadFirst4: []EnumCount{{Key: "zero", Count: 1}, {Key: "large", Count: 1}}},
	} {
		if _, err := EncodeCensus(ctx, c); err == nil {
			t.Fatal("invalid census accepted")
		}
	}
	raw, err := EncodeCensus(ctx, Census{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodeCensus(ctx, Census{}, len(raw)); err != nil {
		t.Fatal("exact census limit refused")
	}
	if _, err := encodeCensus(ctx, Census{}, len(raw)-1); !errors.Is(err, ErrTooLarge) {
		t.Fatal("lower census limit+1 accepted")
	}
	for _, bad := range [][]byte{[]byte("{"), append(append([]byte{}, raw...), '\n'), bytes.Replace(raw, []byte(`"blocks_found":"0"`), []byte(`"blocks_found":"-1"`), 1), bytes.Repeat([]byte(" "), 4194305)} {
		if _, err := DecodeCensus(ctx, bad); err == nil {
			t.Fatal("invalid census bytes accepted")
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := EncodeCensus(cctx, Census{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled census encoded")
	}
	native, selected := sample()
	native.Stats.BlocksFound = -1
	if _, _, err := Build(ctx, native, selected); err == nil {
		t.Fatal("invalid Stats candidate published")
	}
	if _, _, err := Build(cctx, native, selected); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled build published")
	}
}

func TestMeetingAttendeePreallocationBudgets(t *testing.T) {
	ctx := context.Background()
	for _, e := range []Evidence{
		{Attendees: make([]outlookattendees.Attendee, 262145)},
		{Selections: make([]outlookcal.SelectionReceipt, 65537)},
		{Parents: make([]outlookattendees.Evidence, 65537)},
		{AmbiguousKeys: make([]uint32, 262145)},
		{Losses: make([]outlookattendees.Loss, 8)},
	} {
		if _, err := Encode(ctx, e); !errors.Is(err, ErrTooLarge) {
			t.Fatal("oversized array allocated/encoded")
		}
	}
	invalid, long := string([]byte{0xff}), strings.Repeat("a", 262145)
	for _, e := range []Evidence{
		{Selections: []outlookcal.SelectionReceipt{{SourceID: invalid}}},
		{Selections: []outlookcal.SelectionReceipt{{SourceID: long}}},
		{Attendees: []outlookattendees.Attendee{{Name: &long}}},
	} {
		if _, err := Encode(ctx, e); err == nil {
			t.Fatal("invalid text budget accepted")
		}
	}
	var b bytes.Buffer
	if err := writeEvidenceBounded(ctx, &b, Evidence{}, 0); !errors.Is(err, ErrTooLarge) {
		t.Fatal("actual writer cap+1 accepted")
	}
	if err := checkEnvelopeSize(-1, MaxEnvelopeBytes); err == nil {
		t.Fatal("negative SQL length accepted")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Encode(cctx, Evidence{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled evidence encoded")
	}
}

package outlookmeetingattendees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

// malformedModel encodes deliberately invalid semantic fixtures without using
// the producer's pre-allocation guard.
func malformedModel(e Evidence) []byte {
	var b bytes.Buffer
	b.WriteString(`{"format":"outlook-attendee-evidence-v1","selections":[`)
	for i, s := range e.Selections {
		if i > 0 {
			b.WriteByte(',')
		}
		raw, _ := json.Marshal(toSelection(s))
		b.Write(raw)
	}
	b.WriteString(`],"attendees":[`)
	for i, a := range e.Attendees {
		if i > 0 {
			b.WriteByte(',')
		}
		raw, _ := json.Marshal(toNative(a))
		b.Write(raw)
	}
	b.WriteString(`],"ambiguous_keys":[],"parents":[],"losses":[]}`)
	return b.Bytes()
}

func TestMeetingAttendeeAuditProjectionBudgets(t *testing.T) {
	ctx := context.Background()
	large := strings.Repeat("a", 262145)
	if _, err := Audit(ctx, malformedModel(Evidence{Selections: []outlookcal.SelectionReceipt{{SourceID: large}}}), nil); !errors.Is(err, ErrTooLarge) {
		t.Fatal("read bypassed selected-ID scalar budget")
	}
	if _, err := Audit(ctx, malformedModel(Evidence{Attendees: []outlookattendees.Attendee{{Key: 1, DetailKey: 1, Name: &large}}}), nil); !errors.Is(err, ErrTooLarge) {
		t.Fatal("read bypassed native scalar budget")
	}
	e := Evidence{}
	for i := range 65536 {
		e.Selections = append(e.Selections, outlookcal.SelectionReceipt{SourceID: strconv.Itoa(i), DetailKey: uint32(i + 1)})
	}
	e.Attendees = []outlookattendees.Attendee{{Key: 1, DetailKey: 65537}}
	if _, err := Audit(ctx, malformedModel(e), nil); !errors.Is(err, ErrTooLarge) {
		t.Fatal("read exceeded parent/selection key union")
	}
	e = Evidence{Selections: []outlookcal.SelectionReceipt{
		{SourceID: "one", DetailKey: 1, DetailCopies: 1, DetailPresent: true},
		{SourceID: "two", DetailKey: 1, DetailCopies: 2, DetailPresent: true},
	}}
	if _, err := Audit(ctx, malformedModel(e), nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("same-copy detail counters contradicted")
	}
	c := cursor{raw: []byte("0,1]")}
	if err := c.array(ctx, 1, func(int) error { var value int; return c.row(&value) }); !errors.Is(err, ErrTooLarge) {
		t.Fatal("actual inclusive array guard accepted cap+1")
	}
}

type cancellingContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancellingContext) Err() error {
	c.checks--
	if c.checks < 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestMeetingAttendeeDecodeCancellationSweep(t *testing.T) {
	native, selected := sample()
	e, _, err := Build(context.Background(), native, selected)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		ctx, cancel := context.WithCancel(context.Background())
		got, err := Decode(&cancellingContext{Context: ctx, cancel: cancel, checks: i}, raw)
		cancel()
		if err != nil && (!errors.Is(err, context.Canceled) || len(got.Attendees) != 0) {
			t.Fatal("decode cancellation published partial candidate")
		}
	}
}

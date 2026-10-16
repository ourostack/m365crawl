package outlookmeetingattendees

import (
	"context"

	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

func Build(ctx context.Context, native outlookattendees.Result, selections outlookcal.AttendeeSelections) (Evidence, Census, error) {
	if err := ctx.Err(); err != nil {
		return Evidence{}, Census{}, err
	}
	if selections.Err != nil {
		return Evidence{}, Census{}, selections.Err
	}
	e := Evidence{
		Attendees:     make([]outlookattendees.Attendee, len(native.Attendees)),
		AmbiguousKeys: append([]uint32{}, native.AmbiguousKeys...),
		Parents:       append([]outlookattendees.Evidence{}, native.Evidence...),
		Losses:        append([]outlookattendees.Loss{}, native.Losses...),
		Selections:    append([]outlookcal.SelectionReceipt{}, selections.Rows...),
	}
	for i, a := range native.Attendees {
		a.Name, a.Email = cloneText(a.Name), cloneText(a.Email)
		e.Attendees[i] = a
	}
	c := censusOf(native.Stats)
	if _, err := Encode(ctx, e); err != nil {
		return Evidence{}, Census{}, err
	}
	if _, err := EncodeCensus(ctx, c); err != nil {
		return Evidence{}, Census{}, err
	}
	return e, c, nil
}

func cloneText(s *string) *string {
	if s == nil {
		return nil
	}
	copy := *s
	return &copy
}

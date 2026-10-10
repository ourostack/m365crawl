package outlookattendees

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/ourostack/m365crawl/internal/hxstore"
)

type limits struct {
	attendees, selected, keys, scalar int
	stringBytes                       int64
}

func Collect(ctx context.Context, s *hxstore.Store) (Result, error) {
	return collect(ctx, s, limits{attendees: 262144, selected: 524288, keys: 65536, scalar: 256 << 10, stringBytes: 16 << 20})
}

func collect(ctx context.Context, s *hxstore.Store, cap limits) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if s == nil {
		return Result{}, &Error{Code: "outlook_attendees_read_failed"}
	}
	res := Result{}
	losses := map[string]int{}
	parents := map[uint32]struct{}{}
	union := map[uint32]struct{}{}
	details := map[uint32]int{}
	links := map[uint32]int{}
	first := map[uint32]Attendee{}
	ambiguous := map[uint32]struct{}{}
	selected, attendees := 0, 0
	var stringBytes int64
	admitKey := func(key uint32) error {
		if _, exists := union[key]; exists {
			return nil
		}
		if len(union) >= cap.keys {
			return &Error{Code: "outlook_attendees_too_large"}
		}
		union[key] = struct{}{}
		return nil
	}
	stats, err := s.Walk(ctx, hxstore.WalkOptions{}, func(o hxstore.Object) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if o.Class != 0x71 && o.Class != 0x6c && o.Class != 0x6b {
			return nil
		}
		selected++
		if selected > cap.selected {
			return &Error{Code: "outlook_attendees_too_large"}
		}
		switch o.Class {
		case 0x71:
			attendees++
			if attendees > cap.attendees {
				return &Error{Code: "outlook_attendees_too_large"}
			}
			if o.Tag != 0xb8 {
				return &Error{Code: "outlook_attendees_layout_unsupported"}
			}
			if o.Resynced {
				losses["outlook_attendees_resynced"]++
			}
			a, err := mapAttendee(o, cap.scalar)
			if err != nil {
				var size *Error
				if errors.As(err, &size) {
					return size
				}
				losses["outlook_attendees_unmapped"]++
				return nil
			}
			if a.Name != nil {
				stringBytes += int64(len(*a.Name))
			}
			if a.Email != nil {
				stringBytes += int64(len(*a.Email))
			}
			if stringBytes > cap.stringBytes {
				return &Error{Code: "outlook_attendees_too_large"}
			}
			if err := admitKey(a.DetailKey); err != nil {
				return err
			}
			parents[a.DetailKey] = struct{}{}
			if old, exists := first[a.Key]; exists {
				if !sameObservation(old, a) {
					ambiguous[a.Key] = struct{}{}
				}
			} else {
				first[a.Key] = a
			}
			res.Attendees = append(res.Attendees, a)
		case 0x6c, 0x6b:
			tag := uint16(0x348)
			if o.Class == 0x6b {
				tag = 0x455
			}
			if o.Tag != tag {
				return &Error{Code: "outlook_attendees_layout_unsupported"}
			}
			if o.Resynced {
				losses["outlook_attendees_geometry_resynced"]++
			}
			// Walk already validates tag <= object length; the admitted tags
			// cover both native words.
			key, _ := o.U32(20)
			if key == 0 {
				losses["outlook_attendees_geometry_unmapped"]++
				return nil
			}
			if o.Class == 0x6b {
				key, _ = o.U32(180)
				if key == 0 {
					return nil
				}
			}
			if err := admitKey(key); err != nil {
				return err
			}
			if o.Class == 0x6c {
				details[key]++
			} else {
				links[key]++
			}
		}
		return nil
	})
	if err != nil {
		return Result{Stats: stats}, safeReadError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return Result{Stats: stats}, err
	}
	if stats.PayloadBytes > 0 && stats.UnwalkedBytes > stats.PayloadBytes/5 {
		return Result{Stats: stats}, &Error{Code: "outlook_attendees_layout_unsupported"}
	}
	for reason, count := range stats.Rejected {
		if reason != hxstore.RejectTypeOther {
			losses["outlook_blocks_damaged"] += count
		}
	}
	for _, key := range slices.Sorted(maps.Keys(parents)) {
		if err := ctx.Err(); err != nil {
			return Result{Stats: stats}, err
		}
		res.Evidence = append(res.Evidence, Evidence{DetailKey: key, DetailCopies: details[key], EventLinks: links[key]})
		if details[key] == 0 {
			losses["outlook_attendees_detail_missing"]++
		}
		if links[key] == 0 {
			losses["outlook_attendees_event_link_missing"]++
		}
	}
	res.AmbiguousKeys = slices.Sorted(maps.Keys(ambiguous))
	for _, code := range slices.Sorted(maps.Keys(losses)) {
		res.Losses = append(res.Losses, Loss{Code: code, Count: losses[code]})
	}
	if err := ctx.Err(); err != nil {
		return Result{Stats: stats}, err
	}
	res.Stats = stats
	return res, nil
}

func sameObservation(a, b Attendee) bool {
	return a.DetailKey == b.DetailKey && a.VersionRaw == b.VersionRaw && a.Resynced == b.Resynced &&
		sameText(a.Name, b.Name) && sameText(a.Email, b.Email)
}

func sameText(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func safeReadError(ctx context.Context, err error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var e *Error
	if errors.As(err, &e) && (e.Code == "outlook_attendees_too_large" || e.Code == "outlook_attendees_layout_unsupported") {
		return &Error{Code: e.Code}
	}
	return &Error{Code: "outlook_attendees_read_failed", cause: err}
}

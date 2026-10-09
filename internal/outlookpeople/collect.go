package outlookpeople

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/ourostack/m365crawl/internal/hxstore"
)

type ObjectKey struct {
	Class uint16
	Key   uint32
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	// People retains source copies; no current-copy rule is established for
	// the pair class, so sorting must not be mistaken for version selection.
	People []Person
	// AmbiguousKeys have differing mapped copies, including source-version
	// or framing evidence. Consumers must not select one implicitly.
	AmbiguousKeys []ObjectKey
	Losses        []Loss
	Stats         hxstore.Stats
}

type collectLimits struct {
	people      int
	stringBytes int64
}

// Collect reads a private store copy, with bounded observation and string
// retention. It never discovers app files or resolves account identities.
func Collect(ctx context.Context, s *hxstore.Store) (Result, error) {
	return collect(ctx, s, collectLimits{people: 1 << 16, stringBytes: 16 << 20})
}

func collect(ctx context.Context, s *hxstore.Store, limits collectLimits) (Result, error) {
	if s == nil {
		return Result{}, errors.New("outlookpeople: no store")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	res := Result{}
	losses := map[string]int{}
	first := map[ObjectKey]Person{}
	ambiguous := map[ObjectKey]struct{}{}
	var stringBytes int64
	stats, err := s.Walk(ctx, hxstore.WalkOptions{}, func(o hxstore.Object) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if o.Class != 0xd2 && o.Class != 0x32a {
			return nil
		}
		if (o.Class == 0xd2 && o.Tag != 0x15a) || (o.Class == 0x32a && o.Tag != 0xa8) {
			return &hxstore.GuardError{Code: "outlook_people_layout_unsupported"}
		}
		p, err := MapPerson(o)
		if err != nil {
			losses["outlook_people_unmapped"]++
			return nil
		}
		stringBytes += int64(len(p.DisplayName) + len(p.FirstName) + len(p.LastName) +
			len(p.Email) + len(p.AlternateEmail) + len(p.TeamsID))
		if len(res.People) >= limits.people || stringBytes > limits.stringBytes {
			return &hxstore.GuardError{Code: "outlook_people_too_large"}
		}
		key := ObjectKey{Class: p.Class, Key: p.Key}
		if old, exists := first[key]; exists {
			if old != p {
				ambiguous[key] = struct{}{}
			}
		} else {
			first[key] = p
		}
		if p.Resynced {
			losses["outlook_people_resynced"]++
		}
		res.People = append(res.People, p)
		return nil
	})
	if err != nil {
		return Result{Stats: stats}, err
	}
	if stats.PayloadBytes > 0 && (stats.PayloadBytes-stats.UnwalkedBytes)*100 < stats.PayloadBytes*80 {
		return Result{Stats: stats}, &hxstore.GuardError{Code: "outlook_people_layout_unsupported", Detail: "walk_coverage"}
	}
	for reason, n := range stats.Rejected {
		if reason != hxstore.RejectTypeOther {
			losses["outlook_blocks_damaged"] += n
		}
	}
	res.Stats = stats
	slices.SortFunc(res.People, comparePeople)
	res.AmbiguousKeys = slices.Collect(maps.Keys(ambiguous))
	slices.SortFunc(res.AmbiguousKeys, func(a, b ObjectKey) int {
		return cmp.Or(cmp.Compare(a.Class, b.Class), cmp.Compare(a.Key, b.Key))
	})
	for _, code := range slices.Sorted(maps.Keys(losses)) {
		res.Losses = append(res.Losses, Loss{Code: code, Count: losses[code]})
	}
	return res, nil
}

func comparePeople(a, b Person) int {
	order := cmp.Or(
		cmp.Compare(a.Class, b.Class),
		cmp.Compare(a.Key, b.Key),
		cmp.Compare(a.VersionRaw, b.VersionRaw),
		cmp.Compare(a.Parent, b.Parent),
		strings.Compare(a.Email, b.Email),
		strings.Compare(a.TeamsID, b.TeamsID),
		strings.Compare(a.DisplayName, b.DisplayName),
		strings.Compare(a.FirstName, b.FirstName),
		strings.Compare(a.LastName, b.LastName),
		a.RefreshedAt.Compare(b.RefreshedAt),
	)
	if order != 0 || a.Resynced == b.Resynced {
		return order
	}
	if a.Resynced {
		return 1
	}
	return -1
}

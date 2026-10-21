//go:build acceptance && windows

package acceptance

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
)

type owaCalendarExpectedRead struct {
	Rows     []owaEventExpected
	Counts   map[string]int64
	Released bool
}

func owaPutExpectedEvent(rows map[string]owaEventExpected, payloads map[string][32]byte, event owaEventExpected, payload []byte) error {
	id, sum := event.Temporal.UID, sha256.Sum256(payload)
	if prior, found := payloads[id]; found {
		if prior != sum {
			return errors.New("calendar_oracle_duplicate_identity_conflict")
		}
		return nil
	}
	payloads[id], rows[id] = sum, event
	return nil
}

func owaReadExpectedCalendar(ctx context.Context, level, blob string) (out owaCalendarExpectedRead, result error) {
	out.Released = true
	oracle, err := owaNewWindowsZoneOracle(ctx)
	if err != nil {
		return out, err
	}
	rows := map[string]owaEventExpected{}
	payloads := map[string][32]byte{}
	var projectionErr error
	raw, readErr := owaReadCalendarRaw(ctx, level, blob, func(value any, payload []byte) error {
		event, err := owaProjectEvent(ctx, oracle, value)
		if err != nil {
			projectionErr = err
			return err
		}
		projectionErr = owaPutExpectedEvent(rows, payloads, event, payload)
		return projectionErr
	})
	out.Counts, out.Released = raw.Counts, raw.Released
	if readErr != nil || projectionErr != nil || !raw.Released {
		return out, errors.Join(readErr, projectionErr, errors.New("calendar_oracle_read_unproved"))
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if raw.Counts["decode_failed"] != 0 {
		return out, errors.New("calendar_oracle_decode_coverage_refused")
	}
	out.Rows = make([]owaEventExpected, 0, len(rows))
	for _, event := range rows {
		out.Rows = append(out.Rows, event)
	}
	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].Temporal.UID < out.Rows[j].Temporal.UID })
	if err := ctx.Err(); err != nil {
		out.Rows = nil
		return out, err
	}
	return out, nil
}

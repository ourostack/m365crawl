//go:build acceptance && windows

package acceptance

import (
	"context"
	"errors"
	"testing"
)

func TestOWACalendarExpectedReaderIdentityConflicts(t *testing.T) {
	rows, payloads := map[string]owaEventExpected{}, map[string][32]byte{}
	event := owaEventExpected{Temporal: owaTemporalExpected{UID: "aa11"}}
	for range 2 {
		if owaPutExpectedEvent(rows, payloads, event, []byte("synthetic identical payload")) != nil || len(rows) != 1 {
			t.Fatal("identical_duplicate_changed_expected_census")
		}
	}
	if err := owaPutExpectedEvent(rows, payloads, event, []byte("synthetic conflicting payload")); err == nil ||
		err.Error() != "calendar_oracle_duplicate_identity_conflict" || len(rows) != 1 {
		t.Fatal("conflicting_identity_accepted_or_mutated_expected_rows")
	}
}

func TestOWACalendarExpectedReaderCanceledBeforeOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := owaReadExpectedCalendar(ctx, "synthetic-unused-level", "synthetic-unused-blob")
	if !errors.Is(err, context.Canceled) || !out.Released || len(out.Rows) != 0 {
		t.Fatal("canceled_unopened_reader_claimed_usable_expectations")
	}
}

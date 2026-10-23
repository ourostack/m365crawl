//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

func TestOWAMailCensusLeaseTracksAcquisitionAndActualClose(t *testing.T) {
	for _, mode := range []string{"success", "open-error", "partial-open-error", "close-error", "close-panic"} {
		t.Run(mode, func(t *testing.T) {
			open, closeReader := owaCensusOpen, owaCensusClose
			t.Cleanup(func() { owaCensusOpen, owaCensusClose = open, closeReader })
			released, acquired := true, false
			owaCensusOpen = func(level, blob string, opts indexeddb.OpenOptions) (*indexeddb.Origin, error) {
				if released {
					t.Fatal("mail_value_reader_opened_before_lease_was_held")
				}
				acquired = true
				if mode == "open-error" {
					return nil, errors.New("synthetic-open-failed")
				}
				origin, err := open(level, blob, opts)
				if err == nil && mode == "partial-open-error" {
					return origin, errors.New("synthetic-partial-open-failed")
				}
				return origin, err
			}
			owaCensusClose = func(origin *indexeddb.Origin) error {
				if err := closeReader(origin); err != nil {
					return err
				}
				switch mode {
				case "close-error":
					return errors.New("synthetic-close-unproved")
				case "close-panic":
					panic("synthetic-private-close-panic")
				}
				return nil
			}
			p := owaFixtureProfile("mail", "Synthetic")
			census, err := owaCensusLeaseContext(t.Context(), p.Source.LevelDBDir, p.Source.BlobDir, &released)
			if !acquired || released != (mode == "success" || mode == "partial-open-error") {
				t.Fatal("mail_reader_acquisition_or_release_proof_mismatch")
			}
			if mode == "success" {
				if err != nil || len(census.messages) != 5 {
					t.Fatal("complete_mail_census_not_returned")
				}
			} else if err == nil || len(census.messages) != 0 || len(census.folders) != 0 {
				t.Fatal("failed_mail_census_exposed_partial_expectations")
			}
		})
	}
}

func TestOWAMailCensusCanceledBeforeOpenDoesNotHoldLease(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	released := true
	p := owaFixtureProfile("mail", "Synthetic")
	if _, err := owaCensusLeaseContext(ctx, p.Source.LevelDBDir, p.Source.BlobDir, &released); err == nil || !released {
		t.Fatal("unopened_canceled_mail_reader_claimed_held_lease")
	}
}

func TestOWAMailCensusLegacyWrapperPreservesCloseRefusal(t *testing.T) {
	closeReader := owaCensusClose
	t.Cleanup(func() { owaCensusClose = closeReader })
	owaCensusClose = func(origin *indexeddb.Origin) error {
		if err := closeReader(origin); err != nil {
			return err
		}
		return errors.New("synthetic-close-unproved")
	}
	p := owaFixtureProfile("mail", "Synthetic")
	census, err := owaCensusContext(t.Context(), p.Source.LevelDBDir, p.Source.BlobDir)
	if err == nil || err.Error() != "census_close_refused" || len(census.messages) != 5 {
		t.Fatal("legacy_census_close_error_or_partial_result_contract_changed")
	}
}

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestCalendarProductFailurePacketRequiresBoundValidatedData(t *testing.T) {
	cause := errors.New("synthetic-private-cause")
	if text := calendarFailurePacket(cause); strings.Contains(text, "synthetic-private") ||
		text != "owa_calendar_qualification status=unqualified stage=unproved reason=unproved fields=[]" {
		t.Fatal("arbitrary_product_error_disclosed")
	}
	value := protocol.CalendarQualificationFailure{
		Version: protocol.Version, Kind: protocol.CalendarQualificationFailureKind,
		Owner: protocol.Ownership{}, Stage: protocol.StageCalendarSQL, Reason: "archive_mismatch",
		Fields: []string{"synthetic-private"}, Omissions: map[string]int64{},
	}
	err := &calendarProductFailureError{failure: value, cause: cause}
	if text := calendarFailurePacket(err); strings.Contains(text, "synthetic-private") || !errors.Is(err, cause) ||
		err.Error() != "calendar_qualification_unproved" {
		t.Fatal("invalid_product_packet_or_primary_error_contract_failed")
	}
}

func TestCalendarProductSupervisorReportsBoundFailureOnlyAfterRelease(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("synthetic_executable_missing")
	}
	for _, mode := range []string{"diagnostic", "forged-diagnostic", "sql-diagnostic", "mail-diagnostic", "calendar-source-diagnostic"} {
		t.Run(mode, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipt")
			_, runErr := runCalendarQualified(exe, testDigest(t, exe), calendarProductSource(t, mode), t.TempDir(), receipt, 10*time.Second)
			if runErr == nil {
				t.Fatal("failed_product_test_qualified")
			}
			packet := calendarFailurePacket(runErr)
			if strings.Contains(packet, "synthetic-private") {
				t.Fatal("product_diagnostic_disclosed_private_error")
			}
			if mode == "diagnostic" && packet != "owa_calendar_qualification status=unqualified stage=calendar_oracle reason=calendar_oracle_regional_zone_unsupported fields=[]" {
				t.Fatal("verified_product_failure_reason_lost")
			}
			if mode == "forged-diagnostic" && packet != "owa_calendar_qualification status=unqualified stage=unproved reason=unproved fields=[]" {
				t.Fatal("foreign_product_failure_claimed_verified")
			}
			if mode == "sql-diagnostic" && packet != `owa_calendar_qualification status=unqualified stage=calendar_sql reason=archive_mismatch fields=["is_private"]` {
				t.Fatal("fixed_calendar_field_diagnostic_lost")
			}
			if mode == "mail-diagnostic" && packet != `owa_calendar_qualification status=unqualified stage=import_repeat_compare reason=sync_source_refused fields=["mail_coverage"] omissions={"outlook_owa_reader_blob_missing":2,"outlook_owa_source_changed":1}` {
				t.Fatal("fixed_sorted_mail_loss_diagnostic_lost")
			}
			if mode == "calendar-source-diagnostic" && packet != `owa_calendar_qualification status=unqualified stage=import_repeat_compare reason=sync_source_refused fields=["pass_full_read","calendar_omissions"] omissions={"calendar_refused":2,"outlook_event_unmapped":7}` {
				t.Fatal("fixed_calendar_pass_cause_and_loss_packet_lost")
			}
		})
	}
}

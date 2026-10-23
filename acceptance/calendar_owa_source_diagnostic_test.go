//go:build acceptance

package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestOWACalendarSourceFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		mode, field, reason string
		omissions           map[string]int64
	}{
		{"scope", "source_reports", "sync_source_refused", map[string]int64{}},
		{"mail", "mail_source", "sync_source_refused", map[string]int64{}},
		{"calendar", "calendar_source", "sync_source_refused", map[string]int64{}},
		{"error", "calendar_error_outlook_layout_unsupported", "sync_source_refused", map[string]int64{}},
		{"private_error", "calendar_error_unproved", "sync_source_refused", map[string]int64{}},
		{"calendar_loss", "calendar_omissions", "sync_source_refused", map[string]int64{"outlook_event_unmapped": 7}},
		{"private_loss", "calendar_omissions", "unproved", map[string]int64{}},
		{"mail_loss", "mail_omissions", "sync_source_refused", map[string]int64{"outlook_owa_detail_conflict": 2}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			path := filepath.Join(owaScratch(t), "calendar-source-failure.json")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal("synthetic_artifact_failed")
			}
			// #nosec G204 G702 -- Exact current artifact and fixed report-only probe; no source discovery.
			cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestOWACalendarSourceFailureProbe$")
			cmd.Env = append(os.Environ(), "M365CRAWL_REAL_CACHE=0", "OWA_CALENDAR_SOURCE_PROBE="+tc.mode, "OWA_CALENDAR_SOURCE_PATH="+path)
			if cmd.Run() == nil {
				t.Fatal("invalid_source_report_qualified")
			}
			// #nosec G304 G703 -- Exact parent-owned synthetic fixed-code/count receipt.
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal("source_failure_packet_missing")
			}
			var actual struct {
				Stage, Reason string
				Fields        []string
				Omissions     map[string]int64
			}
			if json.Unmarshal(data, &actual) != nil || actual.Stage != protocol.StageImport ||
				actual.Reason != tc.reason || !reflect.DeepEqual(actual.Fields, []string{"pass_full_read", tc.field}) ||
				!reflect.DeepEqual(actual.Omissions, tc.omissions) || strings.Contains(string(data), "synthetic-private") {
				t.Fatal("calendar_source_failure_lost_pass_cause_or_counts")
			}
		})
	}
}

func TestOWACalendarSourceFailureProbe(t *testing.T) {
	mode := os.Getenv("OWA_CALENDAR_SOURCE_PROBE")
	if mode == "" {
		t.Skip("synthetic report-only source diagnostic probe")
	}
	if os.Getenv("M365CRAWL_REAL_CACHE") != "0" {
		t.Fatal("synthetic_real_flag_refused")
	}
	state := &owaFailureState{calendar: true}
	t.Cleanup(func() {
		data, err := json.Marshal(struct {
			Stage, Reason string
			Fields        []string
			Omissions     map[string]int64
		}{state.failure, state.reason, state.fields, state.omissions})
		if err != nil {
			t.Error("synthetic_encode_failed")
			return
		}
		// #nosec G703 -- Exact parent-owned fixed-code/count receipt, never report values.
		if os.WriteFile(os.Getenv("OWA_CALENDAR_SOURCE_PATH"), data, 0o600) != nil {
			t.Error("synthetic_receipt_failed")
		}
	})
	c := owaObservation{profile: outlookowa.Profile{Name: "Synthetic"}, failure: state}
	rep := syncer.Report{Sources: []syncer.SourceReport{
		{Source: "outlook|Synthetic", Status: syncer.StatusOK},
		{Source: "outlook|Synthetic|mail", Status: syncer.StatusOK},
	}}
	switch mode {
	case "scope":
		rep.Sources = append(rep.Sources, rep.Sources[0])
	case "mail":
		rep.Sources = rep.Sources[:1]
	case "calendar":
		rep.Sources = append(rep.Sources[1:], syncer.SourceReport{Source: "synthetic-private"})
	case "error":
		rep.Sources[0].Error = &syncer.SourceError{Code: "outlook_layout_unsupported", Message: "synthetic-private"}
	case "private_error":
		rep.Sources[0].Error = &syncer.SourceError{Code: "synthetic-private", Message: "synthetic-private"}
	case "calendar_loss":
		rep.Sources[0].Omissions = map[string]int{"outlook_event_unmapped": 7}
	case "private_loss":
		rep.Sources[0].Omissions = map[string]int{"synthetic-private": 1}
	case "mail_loss":
		rep.Sources[1].Omissions = map[string]int{"outlook_owa_detail_conflict": 2}
	default:
		t.Fatal("synthetic_mode_refused")
	}
	owaSyncChecks(t, rep, nil, c, 2)
}

func TestOWACalendarSourceFailurePassAndFirstCause(t *testing.T) {
	rep := syncer.Report{Sources: []syncer.SourceReport{
		{Source: "outlook|Synthetic", Error: &syncer.SourceError{Code: "internal"}},
		{Source: "outlook|Synthetic|mail", Omissions: map[string]int{"outlook_owa_detail_conflict": 1}},
	}}
	for _, tc := range []struct {
		step int
		pass string
	}{{0, "pass_initial"}, {1, "pass_unchanged"}, {2, "pass_full_read"}, {-1, "pass_unproved"}} {
		s := &owaFailureState{calendar: true}
		s.calendarSourceFailure(rep, "Synthetic", tc.step)
		s.calendarSourceFailure(syncer.Report{}, "Other", 0)
		if !reflect.DeepEqual(s.fields, []string{tc.pass, "calendar_error_internal"}) || len(s.omissions) != 0 {
			t.Fatal("calendar_first_cause_or_pass_overwritten")
		}
	}
	var absent *owaFailureState
	absent.calendarSourceFailure(rep, "Synthetic", 0)
	s := &owaFailureState{calendar: true}
	rep.Sources[1].Source = "outlook|Other|mail"
	s.calendarSourceFailure(rep, "Synthetic", 0)
	if !reflect.DeepEqual(s.fields, []string{"pass_initial", "mail_source"}) {
		t.Fatal("foreign_mail_report_not_distinguished")
	}
}

//go:build acceptance && windows

package acceptance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookowacal"
	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

const owaPrivateFieldReportName = "calendar-field-types.json"

type owaPrivateFieldReport struct {
	Version    int               `json:"version"`
	Kind       string            `json:"kind"`
	Nonce      string            `json:"nonce"`
	BinaryHash string            `json:"binary_hash"`
	Complete   bool              `json:"complete"`
	Categories map[string]int64  `json:"categories"`
	Fields     []owaPrivateShape `json:"fields"`
	counts     map[[3]string]int64
}

func owaPrivateDiagnostic(t *testing.T, owner protocol.Ownership) *owaPrivateFieldReport {
	t.Helper()
	mode := os.Getenv("CALENDAR_PRIVATE_FIELD_TYPES")
	if mode == "" {
		return nil
	}
	if mode != "1" {
		t.Fatal("private_field_diagnostic_mode_refused")
	}
	out := &owaPrivateFieldReport{
		Version: 1, Kind: "calendar_field_types", Nonce: owner.Nonce, BinaryHash: owner.BinaryHash,
		Categories: map[string]int64{"events": 0, "map_refused": 0, "unmapped": 0, "unknown_zone": 0, "all_day_unaligned": 0, "partial_attendees": 0, "decode_failed": 0},
		counts:     map[[3]string]int64{},
	}
	t.Cleanup(func() {
		current, err := protocol.CurrentCalendarQualificationGuard()
		if err != nil || current != owner || !out.Complete {
			t.Error("private_field_diagnostic_unproved")
			return
		}
		out.Fields = make([]owaPrivateShape, 0, len(out.counts))
		for key, count := range out.counts {
			out.Fields = append(out.Fields, owaPrivateShape{key[0], key[1], key[2], count})
		}
		sort.Slice(out.Fields, func(i, j int) bool {
			a, b := out.Fields[i], out.Fields[j]
			if a.Field != b.Field {
				return a.Field < b.Field
			}
			if a.Type != b.Type {
				return a.Type < b.Type
			}
			return a.Category < b.Category
		})
		data, err := json.Marshal(out)
		if err != nil || protocol.WriteProtectedNew(filepath.Join(filepath.Dir(os.Getenv(protocol.OwnerEnv)), owaPrivateFieldReportName), data) != nil {
			t.Error("private_field_diagnostic_record_refused")
		}
	})
	return out
}

func (out *owaPrivateFieldReport) observe(value any, _ []byte) error {
	if out.Categories["events"] >= 100000 || len(out.counts) > 1024 {
		return errors.New("private_field_diagnostic_budget")
	}
	out.Categories["events"]++
	_, notes, err := outlookowacal.MapEvent("private-diagnostic", value)
	if err != nil {
		out.Categories["map_refused"]++
	} else {
		out.Categories["unmapped"] += int64(notes.Unmapped)
		out.Categories["unknown_zone"] += int64(notes.UnknownZone)
		out.Categories["all_day_unaligned"] += int64(notes.AllDayUnaligned)
		out.Categories["partial_attendees"] += int64(notes.PartialAttendees)
	}
	for _, shape := range owaPrivateShapes(value) {
		out.counts[[3]string{shape.Field, shape.Type, shape.Category}] += shape.Count
	}
	return nil
}

func (out *owaPrivateFieldReport) read(t *testing.T, captured owaObservation, state *owaFailureState) {
	t.Helper()
	if out == nil {
		return
	}
	state.stage = protocol.StageCalendarOracle
	*captured.readersReleased = false
	raw, err := owaReadCalendarRaw(captured.ctx, captured.profile.Source.LevelDBDir, captured.profile.Source.BlobDir, out.observe)
	*captured.readersReleased = raw.Released
	if err != nil || !raw.Released {
		state.record(protocol.StageCalendarOracle, "unproved")
		t.Fatal("private_field_diagnostic_read_refused")
	}
	out.Categories["decode_failed"] = raw.Counts["decode_failed"]
	out.Complete = true
}

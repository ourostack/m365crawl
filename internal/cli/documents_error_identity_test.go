package cli

import (
	"encoding/json"
	"testing"
)

func TestDocumentsMixedRefreshErrorsPreserveEachSourceCode(t *testing.T) {
	notes := notesCoverage(nil)
	docs := (&runtime{}).documentsCoverage(nil)
	notes.RefreshError = &syncError{Code: "note_snapshot_failed", Message: "notes refused"}
	docs.RefreshError = &syncError{Code: "office_json_input_unqualified", Message: "documents refused"}
	raw, err := json.Marshal(mergedOptionalCoverage(notes, docs))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Errors map[string]syncError `json:"refresh_errors"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Errors["notes"].Code != "note_snapshot_failed" || got.Errors["documents"].Code != "office_json_input_unqualified" {
		t.Fatal("mixed refresh discarded independent machine-readable source failure identity")
	}
}

package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPeopleTextSyncAndUnshapedObservation(t *testing.T) {
	var out bytes.Buffer
	rt := &runtime{stdout: &out}
	if err := rt.renderText("people sync", notesSyncResult{Captured: 1, SourceCoverage: peopleCoverage(nil)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "people: 1 captured") {
		t.Fatal("people sync mislabeled")
	}
	if err := rt.renderText("people", newList([]any{peopleObservationItem{
		ID: "person-observation:1", DisplayName: "Fixture", Sources: []string{"outlook_people"}}}, false)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Fixture") {
		t.Fatal("unshaped observation absent")
	}
}

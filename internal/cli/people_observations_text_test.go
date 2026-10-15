package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestPeopleObservationsTextIncludesNativeDetailAndCoverage(t *testing.T) {
	var out bytes.Buffer
	rt := &runtime{stdout: &out}
	item := peopleDetailItem{peopleObservationItem: peopleObservationItem{
		ID: "person-observation:1", DisplayName: "Fixture Person", IdentityState: "unresolved", Sources: []string{"outlook_people"}},
		Copies:      []peopleObservationItem{{Email: "fixture@example.invalid", TeamsID: "8:orgid:fixture"}},
		Explanation: "Profile-owner links do not prove subject tenancy."}
	result := newList([]any{item}, false)
	result.SourceCoverage = peopleCoverage([]store.KnowledgeSource{{ID: 1, Kind: "outlook_people", Coverage: "partial"}})
	if err := rt.renderText("people show", result); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{"Fixture Person", "fixture@example.invalid", "8:orgid:fixture", "unresolved", "subject tenancy", "outlook_native_people_observations"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("people text lacks %q:\n%s", expected, out.String())
		}
	}
}

func TestPeopleObservationsBoundedNamesPreserveIdentityEvidence(t *testing.T) {
	item := peopleItem(store.PeopleObservationRow{Person: outlookpeople.Person{
		DisplayName: "Long fixture display", FirstName: "Fixture", LastName: "Example",
		Email: "fixture@example.invalid", TeamsID: "8:orgid:fixture"}}, 4)
	if !item.TextTruncated || item.DisplayName != "Lon…" || item.Email != "fixture@example.invalid" || item.TeamsID != "8:orgid:fixture" {
		t.Fatalf("bounded names must not truncate identity evidence: %+v", item)
	}
}

package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestPeopleSharedPreparationFailureStaysOptional(t *testing.T) {
	isolateTmp(t)
	root := mailRoot(t)
	old := peopleResolve
	t.Cleanup(func() { peopleResolve = old })
	peopleResolve = func(string) (string, error) { return "", errors.New("people locator") }
	report, _ := run(t, outlookOpts(newDB(t), root))
	found := false
	for _, source := range report.Sources {
		if source.Source == "outlook|Main|people" {
			found = true
			if source.Error == nil {
				t.Fatal("shared preparation error hidden")
			}
		}
	}
	if !found || report.Status == "partial" {
		t.Fatal("optional preparation changed legacy settlement")
	}
	if out := optionalPeopleOutcome("Main", errs.DBError(context.Canceled)); out.report.Error == nil {
		t.Fatal("coded failure hidden")
	}
}

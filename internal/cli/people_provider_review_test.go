package cli

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestPeopleReviewedUnsupportedProviderGivesCopyGuidance(t *testing.T) {
	e := newEnv(t)
	old := runPeopleSync
	t.Cleanup(func() { runPeopleSync = old })
	runPeopleSync = func(context.Context, syncer.PeopleOptions) (syncer.PeopleReport, error) {
		return syncer.PeopleReport{}, &outlookdesktop.NotSupportedError{GOOS: "windows"}
	}
	code, _, stderr := e.run("people", "sync")
	if code != 3 || errorOf(t, stderr)["code"] != "outlook_people_source_unsupported" {
		t.Fatalf("unsupported provider misleading: %d %s", code, stderr)
	}
}

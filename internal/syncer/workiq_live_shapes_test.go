package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

// The synthetic helper reproduces WorkIQ's live plain-text path refusal, which echoes the path.
func TestWorkIQPathRefusalIsAFixedCodeAndNeverStored(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MODE", "refused")
	_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_policy_denied" || strings.Contains(err.Error(), "Access denied") ||
		strings.Contains(err.Error(), "folder-inbox") {
		t.Fatal("path refusal was not a fixed code or echoed its path", err)
	}
	if count(t, db, `select count(*) from workiq_attempts where error_code='workiq_policy_denied'`) != 1 {
		t.Fatal("the refusal was not recorded by its fixed code")
	}
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(filepath.Clean(db + suffix))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "Access denied") {
			t.Fatal("the echoed refusal text reached the archive")
		}
	}
}

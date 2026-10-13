//go:build windows

package officecapture

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Windows has no default usage discovery. A supplied ordinary copy is captured under the same
// held-handle lifetime, and a path whose leaf is spelled in another letter case is refused.
func TestUsageWindowsSuppliedCopyBoundary(t *testing.T) {
	if discovery, err := DiscoverOneDriveUsage(context.Background()); len(discovery.Inputs) != 0 || usageCode(err) != "onedrive_usage_default_unsupported" {
		t.Fatalf("default discovery on Windows: %+v %v", discovery, err)
	}
	path := usageSource(t)
	err := WithOneDriveUsageCapture(context.Background(), UsageInput{Path: path}, func(c *UsageCapture) error {
		if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
			t.Errorf("writer admitted while the supplied copy was held: %s", output)
		}
		_, err := c.ReadUsage(context.Background())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
		t.Fatalf("writer did not recover: %s", output)
	}
	other := filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path)))
	called := false
	err = WithOneDriveUsageCapture(context.Background(), UsageInput{Path: other}, func(*UsageCapture) error { called = true; return nil })
	if called || usageCode(err) != "onedrive_usage_input_unqualified" {
		t.Fatalf("a differently cased leaf was admitted: %v", err)
	}
}

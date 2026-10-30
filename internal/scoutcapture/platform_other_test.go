//go:build !darwin && !linux

package scoutcapture

import (
	"context"
	"testing"
)

func TestAcquisitionIsRefusedUntilVerified(t *testing.T) {
	if _, err := Scan(context.Background(), Options{Root: t.TempDir()}, func(File) error { return nil }); err == nil || err.Error() != "scout_platform_unqualified" {
		t.Fatalf("scan: %v", err)
	}
	if Supported() {
		t.Fatal("supported")
	}
	if _, err := Discover(context.Background(), t.TempDir()); err == nil || err.Error() != "scout_platform_unqualified" {
		t.Fatalf("discover: %v", err)
	}
}

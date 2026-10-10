package protocol

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiagnosticPublicationRequiresCompletedPrivateWrite(t *testing.T) {
	for _, mode := range []string{"pending_refused", "final_exists", "passed"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if MakePrivateDirectory(dir) != nil {
				t.Fatal("private_directory_failed")
			}
			data, _ := Encode(DiagnosticFor(validOwner(), StageCleanup, []string{}))
			if mode == "pending_refused" && WriteProtectedNew(filepath.Join(dir, DiagnosticPendingName), []byte("{}")) != nil {
				t.Fatal("synthetic_pending_block_failed")
			}
			if mode == "final_exists" && WriteProtectedNew(filepath.Join(dir, DiagnosticName), []byte("{}")) != nil {
				t.Fatal("synthetic_final_block_failed")
			}
			err := PublishDiagnostic(dir, data)
			if mode == "passed" {
				if err != nil {
					t.Fatal("completed_diagnostic_publish_failed")
				}
				wire, err := ReadProtected(filepath.Join(dir, DiagnosticName))
				if err != nil {
					t.Fatal("published_diagnostic_missing")
				}
				if _, err := DecodeDiagnostic(wire); err != nil {
					t.Fatal("published_diagnostic_invalid")
				}
			} else if err == nil {
				t.Error("refused_wire_write_published_diagnostic")
			}
			if mode == "pending_refused" {
				if _, err := os.Lstat(filepath.Join(dir, DiagnosticName)); !errors.Is(err, os.ErrNotExist) {
					t.Error("failed_write_exposed_final_diagnostic")
				}
			}
			if mode == "final_exists" {
				wire, err := ReadProtected(filepath.Join(dir, DiagnosticName))
				if err != nil || string(wire) != "{}" {
					t.Error("existing_diagnostic_overwritten")
				}
			}
		})
	}
}

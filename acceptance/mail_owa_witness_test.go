//go:build acceptance

package acceptance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

func TestOWAMailQualificationFixedCensusFields(t *testing.T) {
	c := owaRawCensus{messages: map[string]owaRawMessage{}, folders: map[string]owaRawFolder{}, bodies: map[string]any{}}
	counts := c.counts()
	if len(counts) != len(protocol.CountFields) {
		t.Fatal("qualification_field_count_mismatch")
	}
	for _, field := range protocol.CountFields {
		if _, ok := counts[field]; !ok {
			t.Fatal("qualification_field_missing")
		}
	}
}

func TestOWAMailQualificationCleanupOrdering(t *testing.T) {
	for _, mode := range []string{"passed", "comparison_failed", "cleanup_failed", "write_failed"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(owaScratch(t), "synthetic-witness")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal("test_executable_failed")
			}
			// #nosec G204 -- Exact currently executing test artifact, fixed synthetic-only child selector.
			cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestOWAMailQualificationCleanupProbe$")
			cmd.Env = append(os.Environ(), "M365CRAWL_REAL_CACHE=0", "OWA_WITNESS_PROBE="+mode, "OWA_WITNESS_PATH="+path)
			cmd.Stdout, cmd.Stderr = nil, nil
			err = cmd.Run()
			if mode == "passed" {
				if err != nil {
					t.Fatal("synthetic_probe_failed")
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("post_cleanup_witness_missing")
				}
			} else {
				if err == nil {
					t.Fatal("failed_probe_exit_accepted")
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed_cleanup_or_comparison_emitted_witness")
				}
			}
		})
	}
}

func TestOWAMailQualificationCleanupProbe(t *testing.T) {
	mode := os.Getenv("OWA_WITNESS_PROBE")
	if mode == "" {
		t.Skip("synthetic-only cleanup ordering subprocess")
	}
	if os.Getenv("M365CRAWL_REAL_CACHE") != "0" {
		t.Fatal("synthetic_probe_real_flag_refused")
	}
	cleaned := false
	owaQualificationCleanup(t, func() error {
		if !cleaned {
			return errors.New("cleanup_order_unproved")
		}
		if mode == "write_failed" {
			return errors.New("synthetic_write_refused")
		}
		// #nosec G703 -- Only the parent-selected exact private synthetic scratch witness path.
		return os.WriteFile(os.Getenv("OWA_WITNESS_PATH"), []byte("synthetic-only"), 0o600)
	})
	t.Cleanup(func() {
		cleaned = true
		if mode == "cleanup_failed" {
			t.Error("synthetic_cleanup_failed")
		}
	})
	if mode == "comparison_failed" {
		t.Error("synthetic_comparison_failed")
	}
}

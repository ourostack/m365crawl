package snapshotowner_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/snapshotowner"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestSnapshotOwnerPositiveDeadSweepIntegration(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		dead, remove, legacy, malformed bool
		err                             error
	}{
		{name: "positive-dead", dead: true, remove: true},
		{name: "alive"},
		{name: "pid-reused-live"},
		{name: "permission-unknown", err: fs.ErrPermission},
		{name: "other-unknown", err: errors.New("synthetic process uncertainty")},
		{name: "dead-with-error-retained", dead: true, err: fs.ErrPermission},
		{name: "legacy-retained", legacy: true, dead: true},
		{name: "malformed-retained", malformed: true, dead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(key, tmp)
			}
			queries := 0
			restore := snapshotowner.InstallSyntheticOwnerEvidence(4242, func(pid int) (bool, error) {
				queries++
				if pid != 4242 {
					t.Fatalf("unqualified synthetic PID %d", pid)
				}
				return tc.dead, tc.err
			})
			t.Cleanup(restore)
			dir, err := teamsdesktop.NewPrivateSnapshotRoot()
			if err != nil {
				t.Fatal(err)
			}
			if !tc.legacy {
				if err := snapshotowner.Register(dir); err != nil {
					t.Fatal(err)
				}
			}
			if tc.malformed {
				if err := os.WriteFile(filepath.Join(dir, ".m365crawl-owner.json"), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			payload := filepath.Join(dir, "owned-payload")
			if err := os.WriteFile(payload, []byte("synthetic snapshot payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(dir, old, old); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.remove {
				want = 1
			}
			if got := teamsdesktop.SweepStaleSnapshots(tmp, time.Hour); got != want {
				t.Fatalf("actual owner-qualified sweep removed %d roots, want %d", got, want)
			}
			_, err = os.Stat(payload)
			if tc.remove {
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("positively dead payload survived: %v", err)
				}
			} else if b, err := os.ReadFile(payload); err != nil || string(b) != "synthetic snapshot payload" {
				t.Fatalf("uncertain/live payload was changed: %q %v", b, err)
			}
			wantQueries := 1
			if tc.legacy || tc.malformed {
				wantQueries = 0
			}
			if queries != wantQueries {
				t.Fatalf("process queries=%d, want %d", queries, wantQueries)
			}
		})
	}
}

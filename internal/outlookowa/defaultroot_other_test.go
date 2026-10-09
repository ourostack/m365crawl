//go:build !windows

package outlookowa

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
)

func TestDefaultRootUnsupportedButExplicitRootsWork(t *testing.T) {
	t.Setenv("LOCALAPPDATA", sourceTempDir(t))
	if root, err := DefaultRoot(); !errors.Is(err, outlookdesktop.ErrNotSupported) || root != "" {
		t.Fatalf("non-Windows default enabled: %q %v", root, err)
	}
	root := sourceTempDir(t)
	p := fixtureProfile(t, root, "Default", "mail", false)
	if found, skipped, err := Discover(root); err != nil || len(found) != 1 || len(skipped) != 0 {
		t.Fatalf("explicit portable source: %+v %+v %v", found, skipped, err)
	}
	target := filepath.Join(sourceTempDir(t), "target")
	if err := os.WriteFile(target, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(p.Source.LevelDBDir, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Fingerprint(p); err == nil {
		t.Fatal("symlink fingerprint accepted")
	}
}

type missingNativeIdentity struct{ os.FileInfo }

func (missingNativeIdentity) Sys() any { return nil }

func TestUnixMissingNativeIdentityRefusesFingerprintAndRetries(t *testing.T) {
	restoreSourceIO(t)
	p := fixtureProfile(t, sourceTempDir(t), "Default", "mail", false)
	info := sourceEntryInfo
	sourceEntryInfo = func(entry os.DirEntry) (os.FileInfo, error) {
		fi, err := info(entry)
		if err != nil {
			return nil, err
		}
		return missingNativeIdentity{fi}, nil
	}
	fp, fresh, err := Fingerprint(p)
	if err == nil || err.Error() != "outlookowa: source_unreadable" || fp != "" || !fresh.IsZero() {
		t.Fatalf("unknown native identity vouched for source metadata: %q %v %v", fp, fresh, err)
	}
	sourceEntryInfo = info
	if fp, _, err := Fingerprint(p); err != nil || fp == "" {
		t.Fatalf("restored native metadata could not retry: %q %v", fp, err)
	}
}

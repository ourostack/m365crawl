package outlookdesktop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Versions are the version inputs of a fingerprint. Bumping any of them makes the next sync
// re-read an unchanged store. The caller supplies them, which keeps this package free of the
// reader and mapper packages.
type Versions struct {
	Store  string // the container's version byte as text, for example "i"
	Reader int    // hxstore.ReaderVersion
	Mapper int    // outlookcal.MapperVersion
	Rules  int    // teamsdesktop.RulesVersion
}

// FingerprintOf summarizes the live store file at storePath: a sha256 over the versions and the
// file's size and modification time in nanoseconds. Only HxStore.hxd is looked at; sibling
// files (hxcore.hfl, the lock) are not, because they change without store changes. It stats
// the original and opens nothing.
func FingerprintOf(storePath string, v Versions) (string, error) {
	info, err := statSource(storePath)
	if err != nil {
		return "", mapSourceError(storePath, err)
	}
	if !info.Mode().IsRegular() {
		return "", mapFSError(storePath, fmt.Errorf("%s is not a regular file", storePath))
	}
	return FingerprintFor(info.Size(), info.ModTime(), v), nil
}

// FingerprintFor computes the same fingerprint from a size and modification time already in
// hand, for example the ones an Info from Snapshot carries.
func FingerprintFor(size int64, mod time.Time, v Versions) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "store_version=%s\nreader_version=%d\nmapper_version=%d\nrules_version=%d\nsize=%d\nmtime_ns=%d\n",
		v.Store, v.Reader, v.Mapper, v.Rules, size, mod.UnixNano())
	return hex.EncodeToString(h.Sum(nil))
}

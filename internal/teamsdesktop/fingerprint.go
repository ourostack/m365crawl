package teamsdesktop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ourostack/teamscrawl/internal/errs"
)

// DecoderVersion is part of every fingerprint. Bump it whenever decoding or mapping output
// changes so the next sync re-reads an unchanged cache.
const DecoderVersion = 1

// FingerprintOf summarizes the source's files as a sha256 over decoder_version plus sorted
// "name\tsize\tmtime_ns" lines for both directories, excluding Teams' LOCK and LOG* files
// (Teams rewrites them on every start without changing data).
func FingerprintOf(s Source) (string, error) {
	var lines []string
	for _, d := range []struct{ label, dir string }{{"leveldb", s.LevelDBDir}, {"blob", s.BlobDir}} {
		if d.dir == "" {
			continue
		}
		err := filepath.WalkDir(d.dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				return nil
			}
			base := e.Name()
			if base == "LOCK" || strings.HasPrefix(base, "LOG") {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(d.dir, path)
			if err != nil {
				return err
			}
			lines = append(lines, fmt.Sprintf("%s/%s\t%d\t%d", d.label, filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano()))
			return nil
		})
		if err != nil {
			if d.label == "blob" && errors.Is(err, fs.ErrNotExist) {
				continue // a cache with no blobs has no blob directory
			}
			if errors.Is(err, fs.ErrPermission) {
				return "", errs.NoFullDiskAccess(d.dir, err)
			}
			return "", errs.Internal(fmt.Errorf("fingerprint %s: %w", d.label, err))
		}
	}
	sort.Strings(lines)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "decoder_version=%d\n", DecoderVersion)
	for _, l := range lines {
		_, _ = fmt.Fprintln(h, l)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

package outlookowa

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

var mapperVersion = MapperVersion

// Fingerprint includes source identity, eligible metadata and decoder/rules/mapper versions.
// FreshAt is the latest eligible source file write, never the copy or sync time.
func Fingerprint(p Profile) (string, time.Time, error) {
	if err := ValidateSource(p.Source); err != nil {
		return "", time.Time{}, err
	}
	var lines []string
	var fresh time.Time
	for _, d := range []struct{ label, dir string }{{"leveldb", p.Source.LevelDBDir}, {"blob", p.Source.BlobDir}} {
		if d.dir == "" {
			continue
		}
		err := filepath.WalkDir(d.dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return sourceFailure("source_unreadable", err)
			}
			if !e.IsDir() && (e.Name() == "LOCK" || strings.HasPrefix(e.Name(), "LOG")) {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return sourceFailure("source_unreadable", err)
			}
			if unsafeEntry(info) || !info.IsDir() && !info.Mode().IsRegular() {
				return sourceFailure("unsafe_source", nil)
			}
			id, err := sourceIdentity(path, info)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(d.dir, path)
			if err != nil {
				return sourceFailure("source_unreadable", err)
			}
			if info.IsDir() {
				lines = append(lines, fmt.Sprintf("%s/%s\t%s", d.label, filepath.ToSlash(rel), id))
			} else {
				lines = append(lines, fmt.Sprintf("%s/%s\t%s\t%d\t%d", d.label, filepath.ToSlash(rel), id, info.Size(), info.ModTime().UnixNano()))
				if info.ModTime().After(fresh) {
					fresh = info.ModTime().UTC()
				}
			}
			return nil
		})
		if err != nil {
			return "", time.Time{}, err
		}
	}
	sort.Strings(lines)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "decoder=%d\nrules=%d\nowa_mapper=%d\n", teamsdesktop.DecoderVersion, teamsdesktop.RulesVersion, mapperVersion)
	for _, line := range lines {
		_, _ = fmt.Fprintln(h, line)
	}
	return hex.EncodeToString(h.Sum(nil)), fresh, nil
}

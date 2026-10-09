package outlookowa

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

type Profile struct {
	Name, Dir string
	Source    teamsdesktop.Source
}

type SkippedProfile struct {
	Name, Dir, Reason string
	Err               error
}

var sourceReadDir = os.ReadDir

const (
	levelDBSuffix = ".indexeddb.leveldb"
	blobSuffix    = ".indexeddb.blob"
)

var outlookOrigin = regexp.MustCompile(`^https_outlook\.office\.com_\d+$`)

// sourceError never exports file contents, database names or decoder diagnostics.
type sourceError struct {
	reason string
	cause  error
}

func (e *sourceError) Error() string { return "outlookowa: " + e.reason }
func (e *sourceError) Unwrap() error { return e.cause }

func sourceFailure(reason string, cause error) error {
	return &sourceError{reason: reason, cause: cause}
}

// Discover finds one unambiguous Outlook origin per WebView profile, without opening values.
func Discover(root string) (profiles []Profile, skipped []SkippedProfile, err error) {
	if err := validatePath(root, true); err != nil {
		return nil, nil, err
	}
	ents, err := sourceReadDir(root)
	if err != nil {
		return nil, nil, sourceFailure("root_unreadable", err)
	}
	for _, e := range ents {
		dir := filepath.Join(root, e.Name())
		info, err := os.Lstat(dir)
		if err == nil && info.Mode().IsRegular() {
			continue
		}
		p := Profile{Name: e.Name(), Dir: dir}
		reason := "unsafe_source"
		if err == nil {
			err = validatePath(dir, true)
		}
		if err == nil {
			p.Source, reason, err = discoverSource(p)
		}
		if err != nil {
			skipped = append(skipped, SkippedProfile{Name: p.Name, Dir: dir, Reason: reason, Err: err})
		} else if p.Source.LevelDBDir != "" {
			profiles = append(profiles, p)
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Name < skipped[j].Name })
	return profiles, skipped, nil
}

func discoverSource(p Profile) (teamsdesktop.Source, string, error) {
	idb := filepath.Join(p.Dir, "IndexedDB")
	if _, err := os.Lstat(idb); errors.Is(err, fs.ErrNotExist) {
		return teamsdesktop.Source{}, "", nil
	}
	if err := validatePath(idb, true); err != nil {
		return teamsdesktop.Source{}, "unsafe_source", err
	}
	ents, err := sourceReadDir(idb)
	if err != nil {
		return teamsdesktop.Source{}, "unreadable", sourceFailure("profile_unreadable", err)
	}
	var origins []string
	for _, e := range ents {
		if origin, ok := strings.CutSuffix(e.Name(), levelDBSuffix); ok && outlookOrigin.MatchString(origin) {
			path := filepath.Join(idb, e.Name())
			if err := validatePath(path, true); err != nil {
				return teamsdesktop.Source{}, "unsafe_source", err
			}
			origins = append(origins, origin)
		}
	}
	if len(origins) == 0 {
		return teamsdesktop.Source{}, "", nil
	}
	if len(origins) != 1 {
		return teamsdesktop.Source{}, "origin_ambiguous", sourceFailure("origin_ambiguous", nil)
	}
	s := teamsdesktop.Source{Profile: p.Name, Origin: origins[0], LevelDBDir: filepath.Join(idb, origins[0]+levelDBSuffix)}
	blob := filepath.Join(idb, origins[0]+blobSuffix)
	if _, err := os.Lstat(blob); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return teamsdesktop.Source{}, "unreadable", sourceFailure("profile_unreadable", err)
		}
		s.BlobDir = blob
	}
	if err := ValidateSource(s); err != nil {
		reason := "unsafe_source"
		if errors.Is(err, fs.ErrPermission) {
			reason = "unreadable"
		}
		return teamsdesktop.Source{}, reason, err
	}
	return s, "", nil
}

// ValidateSource rejects links, reparse points and special files before fingerprinting or copying.
func ValidateSource(s teamsdesktop.Source) error {
	if s.LevelDBDir == "" {
		return sourceFailure("source_missing", nil)
	}
	for _, dir := range []string{s.LevelDBDir, s.BlobDir} {
		if dir == "" {
			continue
		}
		if err := validatePath(dir, true); err != nil {
			return err
		}
		err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return sourceFailure("source_unreadable", err)
			}
			info, err := e.Info()
			if err != nil {
				return sourceFailure("source_unreadable", err)
			}
			if unsafeEntry(info) || (!info.IsDir() && !info.Mode().IsRegular()) || (dir == s.LevelDBDir && path != dir && info.IsDir()) {
				return sourceFailure("unsafe_source", nil)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Check every ancestor as well: Lstat of a child alone follows a junction in its parent.
func validatePath(path string, directory bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return sourceFailure("source_unreadable", err)
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return sourceFailure("source_unreadable", err)
		}
		if unsafeEntry(info) || !info.IsDir() && (current != absolute || directory || !info.Mode().IsRegular()) {
			return sourceFailure("unsafe_source", nil)
		}
		if parent := filepath.Dir(current); parent == current {
			return nil
		}
	}
}

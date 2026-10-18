// Package snapshotowner records local snapshot ownership without source or account data.
package snapshotowner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	markerName     = ".m365crawl-owner.json"
	maxMarkerBytes = 1024
	maxOwnerPID    = 1<<31 - 1
)

type marker struct {
	Version int `json:"version"`
	PID     int `json:"pid"`
}

var (
	ownerPID          = os.Getpid
	ownerSelfPID      = os.Getpid
	ownerProcessDead  = platformProcessDead
	ownerLstat        = os.Lstat
	ownerReadFile     = readMarker
	ownerProtected    = platformProtected
	ownerOpenMarker   = platformOpenMarker
	ownerOwnMarker    = platformOwnMarker
	ownerEncodeMarker = func(f *os.File, m marker) error {
		return json.NewEncoder(f).Encode(m)
	}
)

func privateRoot(dir string) (fs.FileInfo, bool) {
	if !filepath.IsAbs(dir) {
		return nil, false
	}
	info, err := ownerLstat(dir)
	return info, err == nil && info.IsDir() && info.Mode()&fs.ModeSymlink == 0 && ownerProtected(dir, info)
}

// Register exclusively writes ownership before any source is copied. The caller
// must dispose of its newly allocated snapshot root if registration fails.
func Register(dir string) (err error) {
	before, ok := privateRoot(dir)
	if !ok {
		return errors.New("snapshot ownership requires a protected, real absolute directory")
	}
	pid := ownerPID()
	if pid <= 0 || pid > maxOwnerPID {
		return errors.New("snapshot ownership requires a positive process identifier")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open snapshot root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return errors.New("snapshot root identity changed before registration")
	}
	f, err := root.OpenFile(markerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create snapshot owner marker: %w", err)
	}
	writeErr := ownerOwnMarker(f)
	if writeErr == nil {
		writeErr = ownerEncodeMarker(f, marker{Version: 1, PID: pid})
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return fmt.Errorf("write snapshot owner marker: %w", err)
	}
	path := filepath.Join(dir, markerName)
	info, err := ownerLstat(path)
	if err != nil || !info.Mode().IsRegular() || !ownerProtected(path, info) {
		return errors.New("snapshot owner marker protection could not be established")
	}
	after, ok := privateRoot(dir)
	if !ok || !os.SameFile(before, after) {
		return errors.New("snapshot root identity changed during registration")
	}
	return nil
}

// Reclaimable grants only ownership evidence, not age or deletion authority.
// Missing, live, reused, foreign or uncertain evidence always retains the root.
func Reclaimable(dir string) bool {
	before, ok := privateRoot(dir)
	if !ok {
		return false
	}
	path := filepath.Join(dir, markerName)
	info, err := ownerLstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxMarkerBytes || !ownerProtected(path, info) {
		return false
	}
	b, err := ownerReadFile(path)
	if err != nil || len(b) > maxMarkerBytes {
		return false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil || m.Version != 1 || m.PID <= 0 || m.PID > maxOwnerPID {
		return false
	}
	dead, err := ownerProcessDead(m.PID)
	if err != nil || !dead {
		return false
	}
	lastMarker, err := ownerLstat(path)
	if err != nil || !lastMarker.Mode().IsRegular() || !os.SameFile(info, lastMarker) || !ownerProtected(path, lastMarker) {
		return false
	}
	after, ok := privateRoot(dir)
	return ok && os.SameFile(before, after)
}

func readMarker(path string) (b []byte, err error) {
	f, err := ownerOpenMarker(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("snapshot owner marker is not a regular file")
	}
	return io.ReadAll(io.LimitReader(f, maxMarkerBytes+1))
}

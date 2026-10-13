//go:build darwin

package officecapture

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

var registryStatat = unix.Fstatat

// sourceParts are the path names of every held edge after the root.
func (s *guardedInput) sourceParts() ([]string, error) {
	parts := strings.Split(strings.TrimPrefix(s.path, "/"), "/")
	if s.directory || len(parts) != len(s.files)-1 || len(s.files) != len(s.stamps) {
		return nil, errors.New("held input is not a bound source file")
	}
	return parts, nil
}

// validateSQLite is the lock-safe validation for a source under SQLite SHARED protection. It
// compares every held descriptor's metadata and every held-parent/name edge without following
// links, and never opens another descriptor for the source inode: on POSIX, closing one would
// release this process's locks.
func (s *guardedInput) validateSQLite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parts, err := s.sourceParts()
	if err != nil {
		return err
	}
	for i, f := range s.files {
		leaf := i == len(s.files)-1
		signature, err := inputSignature(f, !leaf)
		if err != nil || signature != s.stamps[i] {
			return errors.New("held input changed")
		}
		if i == 0 {
			continue
		}
		var held, edge unix.Stat_t
		if err := inputStat(int(f.Fd()), &held); err != nil {
			return err
		}
		if err := registryStatat(int(s.files[i-1].Fd()), parts[i-1], &edge, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if edge.Dev != held.Dev || edge.Ino != held.Ino || edge.Mode&unix.S_IFMT != held.Mode&unix.S_IFMT {
			return errors.New("held edge changed")
		}
		if leaf && (held.Nlink != 1 || edge.Nlink != 1 || edge.Size != held.Size || edge.Flags != held.Flags ||
			edge.Mtim != held.Mtim) {
			return errors.New("held source changed or is linked")
		}
	}
	return nil
}

// registryCompanions inspects only the named SQLite companions, anchored in the held parent.
// Absent or ordinary zero-length journal/WAL files are retained as exact metadata; a nonempty
// journal means a writer is active or recovery is due; WAL content, SHM or links are refused.
func registryCompanions(ctx context.Context, s *guardedInput) (string, error) {
	parts, err := s.sourceParts()
	if err != nil {
		return "", registryError(ctx, "office_registry_input_unqualified")
	}
	parent, leaf := s.files[len(s.files)-2], parts[len(parts)-1]
	var out []string
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		var info unix.Stat_t
		err := registryStatat(int(parent.Fd()), leaf+suffix, &info, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			out = append(out, suffix+":absent")
			continue
		}
		if err != nil || suffix == "-shm" || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 {
			return "", registryError(ctx, "office_registry_input_unqualified")
		}
		if info.Size != 0 && suffix == "-journal" {
			return "", registryError(ctx, "office_registry_source_busy")
		}
		if info.Size != 0 {
			return "", registryError(ctx, "office_registry_input_unqualified")
		}
		out = append(out, fmt.Sprintf("%s:%d:%d:%d:%d:%d:%d", suffix, info.Dev, info.Ino, info.Size,
			info.Mtim.Sec, info.Mtim.Nsec, info.Flags))
	}
	return strings.Join(out, "|"), nil
}

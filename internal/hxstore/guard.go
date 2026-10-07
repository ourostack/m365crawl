package hxstore

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// The codes OpenStore refuses with. Readers of the mail and the calendar share them: a file
// that is not an HxStore, or a store version or page size this reader does not know, turns every
// source in the store off.
const (
	CodeStoreUnrecognized = "outlook_store_unrecognized"
	CodeStoreVersion      = "outlook_store_version"
)

// GuardError is a refusal: the store is not one this reader can read, or it changed layout.
// Code is a stable code and Detail holds numbers and format names only, never text from the
// store.
type GuardError struct {
	Code   string
	Detail string
}

func (e *GuardError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// OpenStore opens the store and maps the container's errors to coded refusals: a file that
// is not an HxStore, a version byte or a page size that is not the known one. Other errors
// (a read failure) are returned as they are.
func OpenStore(r io.ReaderAt, size int64) (*Store, error) {
	s, err := Open(r, size)
	var ev ErrStoreVersion
	var ep ErrPageSize
	switch {
	case err == nil:
		return s, nil
	case errors.Is(err, ErrNotHxStore):
		return nil, &GuardError{Code: CodeStoreUnrecognized}
	case errors.As(err, &ev):
		return nil, &GuardError{Code: CodeStoreVersion, Detail: fmt.Sprintf("version byte 0x%02x, known %s", ev.Found, knownVersions())}
	case errors.As(err, &ep):
		return nil, &GuardError{Code: CodeStoreVersion, Detail: fmt.Sprintf("page size %d, known %d", ep.Found, KnownPageSize)}
	}
	return nil, err
}

func knownVersions() string {
	var parts []string
	for _, v := range KnownStoreVersions {
		parts = append(parts, fmt.Sprintf("0x%02x", v))
	}
	return strings.Join(parts, ",")
}

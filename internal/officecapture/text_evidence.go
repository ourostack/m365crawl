package officecapture

import (
	"encoding/json"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const FileGenerationPolicy = "native-file-incarnation-v1"

// ValidTextRegistration validates an archived native path without using the reader host's path rules.
func ValidTextRegistration(input TextInput, platform string) bool {
	p := input.Path
	if !utf8.ValidString(p) || len(p) > 32768 || strings.ContainsRune(p, 0) || (input.RequestedKind != "docx" && input.RequestedKind != "xlsx" && input.RequestedKind != "pptx") {
		return false
	}
	switch platform {
	case "darwin":
		return strings.HasPrefix(p, "/") && path.Clean(p) == p
	case "windows":
		if len(p) < 4 || ((p[0] < 'A' || p[0] > 'Z') && (p[0] < 'a' || p[0] > 'z')) || p[1] != ':' || p[2] != '\\' ||
			strings.ContainsAny(p[2:], ":/") {
			return false
		}
		for _, part := range strings.Split(p[3:], `\`) {
			if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part {
				return false
			}
		}
		return true
	}
	return false
}

func ValidatePlatformEvidence(platform string, e FileEvidence) error {
	if err := ValidateFileEvidence(e); err != nil {
		return err
	}
	for _, raw := range []*string{e.CreationRaw, e.ModificationRaw} {
		if raw != nil && strings.Contains(*raw, ":") != (platform == "darwin") {
			return errors.New("native clock domain mismatch")
		}
	}
	if (platform != "darwin" && platform != "windows") || (platform == "windows" && e.NativeGenerationRaw != nil) {
		return errors.New("native generation domain mismatch")
	}
	return nil
}

type FileEvidence struct {
	VolumeID, FileID, GenerationKind, GenerationPolicy string
	NativeGenerationRaw, CreationRaw, ModificationRaw  *string
	GenerationQualified                                bool
	Size                                               int64
	ModifiedAt                                         *time.Time
	NativeAttributes                                   string
}

func canonicalUnsigned(value string) (uint64, error) {
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != value {
		return 0, errors.New("invalid unsigned native evidence")
	}
	return n, nil
}

func nativeClock(raw string) (time.Time, bool, error) {
	if sec, nano, ok := strings.Cut(raw, ":"); ok {
		s, err := strconv.ParseInt(sec, 10, 64)
		n, nerr := canonicalUnsigned(nano)
		if err != nil || strconv.FormatInt(s, 10) != sec || nerr != nil || n > 999999999 {
			return time.Time{}, false, errors.New("invalid native clock")
		}
		at := time.Unix(s, int64(n)).UTC()
		return at, (s != 0 || n != 0) && !at.IsZero(), nil
	}
	n, err := canonicalUnsigned(raw)
	if err != nil {
		return time.Time{}, false, err
	}
	// FILETIME is unsigned 100 ns ticks since 1601; split before subtracting.
	// The quotient fits int64 even for maximum uint64 FILETIME; parse the
	// unsigned decimal quotient rather than performing a narrowing conversion.
	seconds, _ := strconv.ParseInt(strconv.FormatUint(n/10000000, 10), 10, 64)
	return time.Unix(seconds-11644473600, int64(n%10000000)*100).UTC(), n != 0, nil
}

func qualifyEvidence(e *FileEvidence) {
	e.GenerationKind, e.GenerationQualified = "unavailable", false
	if e.NativeGenerationRaw != nil && *e.NativeGenerationRaw != "0" {
		e.GenerationKind, e.GenerationQualified = "native_generation", true
	} else if e.CreationRaw != nil && *e.CreationRaw != "0" && *e.CreationRaw != "0:0" {
		e.GenerationKind, e.GenerationQualified = "native_creation", true
	}
}

// ValidateFileEvidence checks canonical native encodings independently of the host reading the archive.
func ValidateFileEvidence(e FileEvidence) error {
	if e.GenerationPolicy != FileGenerationPolicy || e.Size < 0 || e.Size > 64<<20 {
		return errors.New("invalid native evidence policy or size")
	}
	for _, value := range []string{e.VolumeID, e.FileID, e.NativeAttributes} {
		if _, err := canonicalUnsigned(value); err != nil {
			return err
		}
	}
	if e.VolumeID == "0" || e.FileID == "0" {
		return errors.New("unavailable native identity")
	}
	if e.ModifiedAt != nil {
		if _, err := e.ModifiedAt.MarshalJSON(); err != nil {
			return errors.New("unsupported parsed native clock")
		}
	}
	if e.NativeGenerationRaw != nil {
		if _, err := canonicalUnsigned(*e.NativeGenerationRaw); err != nil {
			return err
		}
	}
	for _, raw := range []*string{e.CreationRaw, e.ModificationRaw} {
		if raw != nil {
			if _, _, err := nativeClock(*raw); err != nil {
				return err
			}
		}
	}
	expected := e
	qualifyEvidence(&expected)
	if e.GenerationKind != expected.GenerationKind || e.GenerationQualified != expected.GenerationQualified {
		return errors.New("invalid native continuity")
	}
	if e.ModificationRaw == nil {
		if e.ModifiedAt != nil {
			return errors.New("unbound native clock")
		}
	} else {
		at, known, err := nativeClock(*e.ModificationRaw)
		if err != nil || (e.ModifiedAt != nil && (!known || !e.ModifiedAt.Equal(at))) {
			return errors.New("native clock disagreement")
		}
	}
	return nil
}

func evidenceJSON(e FileEvidence) string {
	b, _ := json.Marshal(e)
	return string(b)
}

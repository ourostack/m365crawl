package officecapture

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/officeregistry"
	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

// Finite consumer policy for the Office registry source, not observed maxima.
const (
	registryDirectoryNames = 4096
	registryLeafLimit      = 16
	registryLeafPrefix     = "MicrosoftRegistrationDB_"
	registryLeafSuffix     = ".reg"
)

var (
	registryLimit     int64 = 64 << 20
	registryReadIndex       = officeregistry.ReadIndex
)

// registryCodes are the fixed failure codes of registry acquisition, most specific first.
var registryCodes = []string{"office_registry_policy_restore_failed", "office_registry_cleanup_failed",
	"office_registry_source_busy", "office_registry_index_too_large", "office_registry_index_unrecognized",
	"office_registry_index_unreadable", "office_registry_default_unsupported", "office_registry_unavailable",
	"office_registry_input_unqualified", "office_registry_capture_failed"}

// guardCodes translate the shared platform guard's fixed codes into registry codes.
var guardCodes = map[string]string{"office_json_policy_restore_failed": "office_registry_policy_restore_failed",
	"office_json_input_unqualified": "office_registry_input_unqualified",
	"office_json_capture_failed":    "office_registry_capture_failed"}

func registryError(ctx context.Context, code string) error {
	return errors.Join(&ReadError{Code: code}, ctx.Err())
}

func fixedCodes(err error, out map[string]bool) {
	switch typed := err.(type) { //nolint:errorlint // walks the joined tree itself to see every fixed code
	case *ReadError:
		out[firstNonEmpty(guardCodes[typed.Code], typed.Code)] = true
	case *officeregistry.ReadError:
		out[typed.Code] = true
	case *onedriveusage.ReadError:
		out[typed.Code] = true
	case interface{ Unwrap() []error }:
		for _, inner := range typed.Unwrap() {
			fixedCodes(inner, out)
		}
	case interface{ Unwrap() error }:
		fixedCodes(typed.Unwrap(), out)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// registryFailure keeps the most specific fixed registry code in err, or capture_failed.
func registryFailure(ctx context.Context, err error) error {
	codes := map[string]bool{}
	fixedCodes(err, codes)
	for _, code := range registryCodes {
		if codes[code] {
			return registryError(ctx, code)
		}
	}
	return registryError(ctx, "office_registry_capture_failed")
}

type RegistryInput struct{ Path string }

// RegistryCapture is one guarded private copy of a registry SQLite source: the shared rollback
// capture with the registry's reader.
type RegistryCapture struct{ *rollbackCapture }

func validRegistryPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func DiscoverRegistry(ctx context.Context) (inputs []RegistryInput, result error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !discoveryMac {
		return nil, registryError(ctx, "office_registry_default_unsupported")
	}
	result = withSourceLifetime(ctx, "office_registry_input_unqualified", func() error {
		return withInputGuard(ctx, func() error {
			home, err := discoveryHome()
			if err != nil {
				return registryError(ctx, "office_registry_input_unqualified")
			}
			directory := filepath.Join(home, "Library", "Group Containers", "UBF8T346G9.Office", "MicrosoftRegistrationDB")
			input, err := openInput(directory, true)
			if errors.Is(err, os.ErrNotExist) {
				return registryError(ctx, "office_registry_unavailable")
			}
			if err != nil {
				return registryError(ctx, "office_registry_input_unqualified")
			}
			names, readErr := discoveryRead(input.file(), registryDirectoryNames+1)
			closeErr := input.close()
			if len(names) > registryDirectoryNames || (readErr != nil && !errors.Is(readErr, io.EOF)) ||
				closeErr != nil || ctx.Err() != nil {
				return registryError(ctx, "office_registry_input_unqualified")
			}
			for _, name := range names {
				if len(name) > len(registryLeafPrefix)+len(registryLeafSuffix) &&
					strings.HasPrefix(name, registryLeafPrefix) && strings.HasSuffix(name, registryLeafSuffix) {
					inputs = append(inputs, RegistryInput{Path: filepath.Join(directory, name)})
				}
			}
			if len(inputs) > registryLeafLimit {
				return registryError(ctx, "office_registry_input_unqualified")
			}
			if len(inputs) == 0 {
				return registryError(ctx, "office_registry_unavailable")
			}
			slices.SortFunc(inputs, func(a, b RegistryInput) int { return strings.Compare(a.Path, b.Path) })
			return nil
		})
	})
	if result != nil {
		return nil, registryFailure(ctx, result)
	}
	return inputs, nil
}

// ObserveRegistry is retry evidence from held metadata: the source stamp and its anchored
// companions. It never reads content.
func ObserveRegistry(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	signature, err := observeRollback(ctx, path)
	if err != nil {
		return "", registryFailure(ctx, err)
	}
	return signature, nil
}

// WithRegistryCapture holds the source lifetime, the platform guard, the original source chain and
// SQLite SHARED protection from admission through cleanup. consume may stage the decoded result
// but must not publish it: success means every late validation, private removal, unlock, source
// close and policy restoration has already succeeded.
func WithRegistryCapture(ctx context.Context, input RegistryInput, consume func(*RegistryCapture) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !validRegistryPath(input.Path) || consume == nil {
		return registryError(ctx, "office_registry_input_unqualified")
	}
	err := withSourceLifetime(ctx, "office_registry_capture_failed", func() error {
		return withInputGuard(ctx, func() error { return captureRegistry(ctx, input, consume) })
	})
	if err != nil {
		return registryFailure(ctx, err)
	}
	return nil
}

// captureRegistry is the shared rollback capture under the registry's 64 MiB limit.
func captureRegistry(ctx context.Context, input RegistryInput, consume func(*RegistryCapture) error) error {
	return captureRollback(ctx, input.Path, registryLimit, "registry.db", func(c *rollbackCapture) error {
		if err := consume(&RegistryCapture{c}); err != nil {
			return registryFailure(ctx, err)
		}
		return nil
	})
}

// ReadRegistry parses exactly the bound private copy with the delivered reader's limits. The
// held source is validated before and after the reader, and the private copy is compared with
// the captured digest on both sides.
func (c *RegistryCapture) ReadRegistry(ctx context.Context) (officeregistry.Result, error) {
	return readBound(ctx, c.rollbackCapture, registryReadIndex, registryFailure)
}

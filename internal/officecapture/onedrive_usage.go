package officecapture

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

// Finite consumer policy for the OneDrive usage source, not observed maxima. At most 16 account
// directories each hold at most one usage leaf and one library leaf, so at most 32 matching
// leaves are ever named before anything is copied.
const (
	usageDirectoryNames = 4096
	usageAccountLimit   = 16
	usageLeaf           = "Microsoft.FileUsageSync.db"
)

var (
	usageLimit     int64 = 512 << 20
	usageReadIndex       = onedriveusage.ReadIndex
	// usageAccount is the one account directory grammar default discovery admits. The spelling is
	// kept as the directory lists it: it is a local name, never an account or tenant identity.
	usageAccount = regexp.MustCompile(`^Business_[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}$`)
)

// usageCodes are the fixed failure codes of usage acquisition, most specific first.
var usageCodes = []string{"onedrive_usage_policy_restore_failed", "onedrive_usage_cleanup_failed",
	"onedrive_usage_source_busy", "onedrive_usage_index_too_large", "onedrive_usage_index_unrecognized",
	"onedrive_usage_index_unreadable", "onedrive_usage_default_unsupported", "onedrive_usage_unavailable",
	"onedrive_usage_input_unqualified", "onedrive_usage_capture_failed"}

func usageError(ctx context.Context, code string) error {
	return errors.Join(&ReadError{Code: code}, ctx.Err())
}

// usageFailure keeps the most specific fixed usage code in err, or capture_failed. The shared
// rollback capture reports the registry's codes; each has exactly one usage code.
func usageFailure(ctx context.Context, err error) error {
	shared := map[string]bool{}
	fixedCodes(err, shared)
	for _, code := range usageCodes {
		if shared[code] || shared["office_registry_"+strings.TrimPrefix(code, "onedrive_usage_")] {
			return usageError(ctx, code)
		}
	}
	return usageError(ctx, "onedrive_usage_capture_failed")
}

type UsageInput struct{ Path string }

// UsageDiscoveryOutcome is one named default location that yielded no input: an account
// directory whose name this version does not read, or an admitted account without a usage file.
type UsageDiscoveryOutcome struct{ Locator, Code string }

// UsageDiscovery is the default usage inputs plus the scoped outcomes found beside them. An
// outcome never stands for a source: nothing was opened or registered for it.
type UsageDiscovery struct {
	Inputs   []UsageInput
	Outcomes []UsageDiscoveryOutcome
}

// UsageCapture is one guarded private copy of a OneDrive usage SQLite source: the shared rollback
// capture with the usage reader.
type UsageCapture struct{ *rollbackCapture }

// usageNames lists one held directory of at most usageDirectoryNames entries.
func usageNames(ctx context.Context, path string) ([]string, error) {
	input, err := openInput(path, true)
	if err != nil {
		return nil, err
	}
	names, readErr := discoveryRead(input.file(), usageDirectoryNames+1)
	closeErr := input.close()
	if len(names) > usageDirectoryNames || (readErr != nil && !errors.Is(readErr, io.EOF)) ||
		closeErr != nil || ctx.Err() != nil {
		return nil, errors.New("unqualified directory")
	}
	return names, nil
}

// DiscoverOneDriveUsage names the default Mac usage files: the exact Microsoft.FileUsageSync.db
// leaf of each account directory under ~/Library/Application Support/OneDrive/settings/ListSync.
// A missing root or leaf is unavailable; an unsafe matching account or leaf, or more entries or
// accounts than the policy admits, refuses the whole family and returns no input. It reads
// directory names and held metadata only, never content.
func DiscoverOneDriveUsage(ctx context.Context) (discovery UsageDiscovery, result error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !discoveryMac {
		return UsageDiscovery{}, usageError(ctx, "onedrive_usage_default_unsupported")
	}
	unqualified := &ReadError{Code: "onedrive_usage_input_unqualified"}
	result = withSourceLifetime(ctx, "onedrive_usage_input_unqualified", func() error {
		return captureGuard(ctx, func() error {
			home, err := discoveryHome()
			if err != nil {
				return unqualified
			}
			root := filepath.Join(home, "Library", "Application Support", "OneDrive", "settings", "ListSync")
			names, err := usageNames(ctx, root)
			if errors.Is(err, os.ErrNotExist) {
				return &ReadError{Code: "onedrive_usage_unavailable"}
			}
			if err != nil {
				return unqualified
			}
			var accounts []string
			for _, name := range names {
				switch {
				case usageAccount.MatchString(name):
					accounts = append(accounts, name)
				case strings.HasPrefix(name, "Business_") || strings.HasPrefix(name, "Personal_"):
					discovery.Outcomes = append(discovery.Outcomes, UsageDiscoveryOutcome{
						Locator: filepath.Join(root, name), Code: "onedrive_usage_discovery_unsupported"})
				}
			}
			if len(accounts) > usageAccountLimit {
				return unqualified
			}
			for _, account := range accounts {
				leaves, err := usageNames(ctx, filepath.Join(root, account))
				if err != nil {
					return unqualified
				}
				path := filepath.Join(root, account, usageLeaf)
				if !slices.Contains(leaves, usageLeaf) {
					discovery.Outcomes = append(discovery.Outcomes, UsageDiscoveryOutcome{Locator: path, Code: "onedrive_usage_unavailable"})
					continue
				}
				// Qualification only: the leaf must be an ordinary local file, not a link or a placeholder.
				leaf, err := openInput(path, false)
				if err != nil || leaf.close() != nil {
					return unqualified
				}
				discovery.Inputs = append(discovery.Inputs, UsageInput{Path: path})
			}
			if len(discovery.Inputs)+len(discovery.Outcomes) == 0 {
				return &ReadError{Code: "onedrive_usage_unavailable"}
			}
			slices.SortFunc(discovery.Inputs, func(a, b UsageInput) int { return strings.Compare(a.Path, b.Path) })
			slices.SortFunc(discovery.Outcomes, func(a, b UsageDiscoveryOutcome) int { return strings.Compare(a.Locator, b.Locator) })
			return nil
		})
	})
	if result != nil {
		return UsageDiscovery{}, usageFailure(ctx, result)
	}
	return discovery, nil
}

// ObserveOneDriveUsage is retry evidence from held metadata: the source stamp and its anchored
// companions. It never reads content.
func ObserveOneDriveUsage(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	signature, err := observeRollback(ctx, path)
	if err != nil {
		return "", usageFailure(ctx, err)
	}
	return signature, nil
}

// WithOneDriveUsageCapture holds the source lifetime, the platform guard, the original source
// chain and SQLite SHARED protection from admission through cleanup, under the usage reader's
// 512 MiB limit. consume may stage the decoded result but must not publish it: success means
// every late validation, private removal, unlock, source close and policy restoration has
// already succeeded.
func WithOneDriveUsageCapture(ctx context.Context, input UsageInput, consume func(*UsageCapture) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !validRegistryPath(input.Path) || consume == nil {
		return usageError(ctx, "onedrive_usage_input_unqualified")
	}
	err := withSourceLifetime(ctx, "onedrive_usage_capture_failed", func() error {
		return captureGuard(ctx, func() error {
			return captureRollback(ctx, input.Path, usageLimit, "usage.db", func(c *rollbackCapture) error {
				return consume(&UsageCapture{c})
			})
		})
	})
	if err != nil {
		return usageFailure(ctx, err)
	}
	return nil
}

// ReadUsage parses exactly the bound private copy with the delivered usage reader and its
// unchanged limits. The held source is validated before and after the reader, and the private
// copy is compared with the captured digest on both sides.
func (c *UsageCapture) ReadUsage(ctx context.Context) (onedriveusage.Result, error) {
	result, err := readBound(ctx, c.rollbackCapture, usageReadIndex, usageFailure)
	if err != nil {
		return onedriveusage.Result{}, errors.Join(usageFailure(c.ctx, err), ctx.Err())
	}
	return result, nil
}

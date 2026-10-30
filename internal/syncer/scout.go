package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ourostack/m365crawl/internal/scoutcapture"
	"github.com/ourostack/m365crawl/internal/scoutsessions"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// ScoutOptions select the app folders `scout sync` reads: Roots when given, otherwise the folders
// discovered under Home's local OneDrive folders.
type ScoutOptions struct {
	DBPath string
	Roots  []string
	Home   string
}

// ScoutFolderReport is what one app folder's scan did. Outcome is complete (every listed session
// is archived as it is now), partial (some file or device folder was not read), failed (the folder
// could not be scanned; Reason says why) or cancelled. SourceID is zero for a folder refused before
// it was registered.
type ScoutFolderReport struct {
	SourceID                                                                         int64
	Root                                                                             string
	Outcome, Reason                                                                  string
	Devices, UnreadableDevices                                                       int
	Listed, Admitted, Unchanged, CloudOnly, Unsafe, TooLarge, Failed, Gone, Redacted int
}

// ScoutReport lists every folder in the order read. Discovered says the folders were found
// rather than named.
type ScoutReport struct {
	Discovered bool
	Folders    []ScoutFolderReport
}

// ErrScoutFolderNotFound means discovery found no app folder with a Scout session layout.
var ErrScoutFolderNotFound = &scoutcapture.Error{Code: "scout_folder_not_found"}

var (
	scoutScan      = scoutcapture.Scan
	scoutDiscover  = scoutcapture.Discover
	scoutSupported = scoutcapture.Supported
	scoutOpen      = store.Open
	scoutAcquire   = store.AcquireLock
	scoutPlatform  = runtime.GOOS
	scoutNow       = time.Now
)

// SyncScout reads the selected app folders into the archive. Each resident session file is read
// once, digested, scrubbed of credential-looking values and parsed; cloud-only files are counted
// and never opened. A folder that cannot be scanned is reported as failed and the others are still
// read. The error is the archive's, the context's or discovery's; a run that fails after writing
// still returns what it committed.
func SyncScout(ctx context.Context, opts ScoutOptions) (ScoutReport, error) {
	var report ScoutReport
	if !scoutSupported() {
		return report, scoutcapture.ErrPlatformUnqualified
	}
	roots := opts.Roots
	if len(roots) == 0 {
		found, err := scoutDiscover(ctx, opts.Home)
		if err != nil {
			return report, err
		}
		if len(found) == 0 {
			return report, ErrScoutFolderNotFound
		}
		roots, report.Discovered = found, true
	}
	release, err := scoutAcquire(opts.DBPath)
	if err != nil {
		return report, err
	}
	defer release()
	st, err := scoutOpen(ctx, opts.DBPath)
	if err != nil {
		return report, err
	}
	for _, root := range roots {
		folder, err := syncScoutFolder(ctx, st, root)
		report.Folders = append(report.Folders, folder)
		if err != nil {
			return report, errors.Join(err, st.Close())
		}
	}
	return report, st.Close()
}

// scoutLayout resolves root and checks that it holds sessions/devices, so a mistyped folder is
// refused before it is registered.
func scoutLayout(root string) (string, bool) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	info, err := os.Lstat(filepath.Join(resolved, "sessions", "devices"))
	return resolved, err == nil && info.IsDir()
}

func syncScoutFolder(ctx context.Context, st *store.Store, root string) (ScoutFolderReport, error) {
	out := ScoutFolderReport{Root: root, Outcome: "failed"}
	resolved, ok := scoutLayout(root)
	if !ok {
		out.Reason = "scout_root_invalid"
		return out, nil
	}
	out.Root = resolved
	run, err := st.BeginScoutRun(ctx, scoutPlatform, resolved, scoutNow())
	if err != nil {
		return out, err
	}
	out.SourceID = run.SourceID
	known := make(map[scoutcapture.Key]scoutcapture.Known, len(run.Known))
	for key, k := range run.Known {
		known[scoutcapture.Key{Device: key.Device, Name: key.Name}] = scoutcapture.Known{Size: k.Size, ModNanos: k.ModNanos}
	}
	result, scanErr := scoutScan(ctx, scoutcapture.Options{Root: resolved, Known: known, MaxBytes: scoutsessions.MaxFileBytes}, func(f scoutcapture.File) error {
		in := store.ScoutFileInput{Device: f.Device, Name: f.Name, State: f.State, Reason: f.Reason, Size: f.Size, Modified: f.Modified}
		if f.State == scoutcapture.StateRead {
			sum := sha256.Sum256(f.Data)
			in.Digest = hex.EncodeToString(sum[:])
			clean, redacted := teamsdesktop.Scrub(f.Data)
			if session, err := scoutsessions.Parse(clean); err == nil {
				in.Session, in.Raw, in.Redactions = &session, clean, redacted
				out.Redacted += redacted
			}
		}
		outcome, err := st.CommitScoutFile(ctx, run, in, scoutNow())
		if err != nil {
			return err
		}
		out.Listed++
		switch outcome {
		case "admitted":
			out.Admitted++
		case "unchanged":
			out.Unchanged++
		case store.ScoutCloudOnly:
			out.CloudOnly++
		case store.ScoutUnsafe:
			out.Unsafe++
		case store.ScoutTooLarge:
			out.TooLarge++
		default:
			out.Failed++
		}
		return nil
	})
	end := store.ScoutRunEnd{DevicesListed: scanErr == nil, Listed: out.Listed, Admitted: out.Admitted, Unchanged: out.Unchanged,
		CloudOnly: out.CloudOnly, Failed: out.Failed + out.Unsafe + out.TooLarge}
	for _, device := range result.Devices {
		end.SeenDevices = append(end.SeenDevices, device.ID)
		if device.Listed {
			end.ListedDevices = append(end.ListedDevices, device.ID)
		}
	}
	out.Devices, out.UnreadableDevices = len(end.SeenDevices), len(end.SeenDevices)-len(end.ListedDevices)
	var coded *scoutcapture.Error
	var fatal error
	switch {
	case scanErr == nil && end.Failed+end.CloudOnly+out.UnreadableDevices == 0:
		out.Outcome = "complete"
	case scanErr == nil:
		out.Outcome = "partial"
	case errors.As(scanErr, &coded):
		out.Reason = coded.Code
	case ctx.Err() != nil:
		out.Outcome, fatal = "cancelled", scanErr
	default:
		fatal = scanErr
	}
	end.Outcome, end.Reason = out.Outcome, out.Reason
	out.Gone, err = st.FinishScoutRun(context.WithoutCancel(ctx), run, end, scoutNow())
	return out, errors.Join(fatal, err)
}

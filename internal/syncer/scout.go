package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/ourostack/m365crawl/internal/scoutcapture"
	"github.com/ourostack/m365crawl/internal/scoutsessions"
	"github.com/ourostack/m365crawl/internal/store"
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
	SourceID                                                                                     int64
	Root                                                                                         string
	Outcome, Reason                                                                              string
	Devices, UnreadableDevices                                                                   int
	Listed, Admitted, Unchanged, Rescrubbed, CloudOnly, Unsafe, TooLarge, Failed, Gone, Redacted int
}

// ScoutReport lists every folder in the order read. Discovered says the folders were found
// rather than named. Rescrubbed counts archived observations, gone ones included, that were
// stored under an older parser or scrub stamp and were scrubbed and mapped again in place.
type ScoutReport struct {
	Discovered bool
	Rescrubbed int
	Folders    []ScoutFolderReport
}

// ErrScoutFolderNotFound means discovery found no app folder with a Scout session layout.
var ErrScoutFolderNotFound = &scoutcapture.Error{Code: "scout_folder_not_found"}

var (
	scoutScan  = scoutcapture.Scan
	scoutCheck = scoutcapture.Check
	scoutStamp = scoutsessions.Stamp
	scoutScrub = scoutsessions.Scrub
	// scoutCheckpoint is store.CheckpointScoutRescrub; tests make it fail.
	scoutCheckpoint = (*store.Store).CheckpointScoutRescrub
	scoutDiscover   = scoutcapture.Discover
	scoutSupported  = scoutcapture.Supported
	scoutOpen       = store.Open
	scoutAcquire    = store.AcquireLock
	scoutPlatform   = runtime.GOOS
	scoutNow        = time.Now
)

// RescrubScoutArchive scrubs and maps again every archived Scout observation stored under another
// stamp (scoutsessions.Stamp), gone files included, then clears the write-ahead log. It reads only
// the archive: both scout sync and sync run it, as sync runs the Teams credential purge, so a rule
// change reaches the archive even when no Scout folder can be read any more.
func RescrubScoutArchive(ctx context.Context, st *store.Store) (int, error) {
	n, err := st.RescrubScout(ctx, scoutStamp(), scoutScrub)
	if err == nil && n > 0 {
		err = scoutCheckpoint(st, ctx)
	}
	return n, err
}

// SyncScout reads the selected app folders into the archive. When the archive exists it first
// re-scrubs it (RescrubScoutArchive), before the platform check and discovery, so an archive with
// no reachable Scout folder still loses secrets a newer rule covers. Then each resident session
// file whose size, time or stamp changed is read once, digested, scrubbed (scoutsessions.Scrub)
// and parsed; cloud-only files are counted and never opened. A folder that cannot be scanned is
// reported as failed and the others are still read. The error is the archive's, the context's,
// the platform's or discovery's; a run that fails after writing still returns what it committed.
func SyncScout(ctx context.Context, opts ScoutOptions) (report ScoutReport, err error) {
	var st *store.Store
	release := func() {}
	defer func() {
		if st != nil {
			err = errors.Join(err, st.Close())
		}
		release()
	}()
	open := func() error {
		r, err := scoutAcquire(opts.DBPath)
		if err != nil {
			return err
		}
		release = r
		st, err = scoutOpen(ctx, opts.DBPath)
		return err
	}
	if _, statErr := os.Stat(opts.DBPath); statErr == nil {
		if err = open(); err != nil {
			return report, err
		}
		if report.Rescrubbed, err = RescrubScoutArchive(ctx, st); err != nil {
			return report, err
		}
	}
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
	if st == nil {
		if err = open(); err != nil {
			return report, err
		}
	}
	for _, root := range roots {
		folder, err := syncScoutFolder(ctx, st, root)
		report.Folders = append(report.Folders, folder)
		if err != nil {
			return report, err
		}
	}
	return report, nil
}

func syncScoutFolder(ctx context.Context, st *store.Store, root string) (ScoutFolderReport, error) {
	out := ScoutFolderReport{Root: root, Outcome: "failed"}
	// The layout is checked, inside the no-download guard, before the folder is registered, so
	// a mistyped folder never becomes a source.
	resolved, err := scoutCheck(ctx, root)
	var coded *scoutcapture.Error
	if errors.As(err, &coded) {
		out.Reason = coded.Code
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Root = resolved
	run, err := st.BeginScoutRun(ctx, scoutPlatform, resolved, scoutStamp(), scoutNow())
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
			clean, redacted := scoutScrub(f.Data)
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
		case "rescrubbed":
			out.Rescrubbed++
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
	end := store.ScoutRunEnd{DevicesListed: scanErr == nil, Listed: out.Listed, Admitted: out.Admitted, Unchanged: out.Unchanged + out.Rescrubbed,
		CloudOnly: out.CloudOnly, Failed: out.Failed + out.Unsafe + out.TooLarge}
	for _, device := range result.Devices {
		end.SeenDevices = append(end.SeenDevices, device.ID)
		if device.Listed {
			end.ListedDevices = append(end.ListedDevices, device.ID)
		}
	}
	out.Devices, out.UnreadableDevices = len(end.SeenDevices), len(end.SeenDevices)-len(end.ListedDevices)
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

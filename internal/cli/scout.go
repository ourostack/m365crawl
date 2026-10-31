package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/scoutcapture"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

type scoutGroup struct {
	Sync    scoutSyncCmd    `cmd:"" help:"Opt in: copy Microsoft Scout session files already on this computer, in your local OneDrive folder, into the archive. Never downloads a cloud-only file."`
	List    scoutListCmd    `cmd:"" help:"List archived Scout sessions, most recent activity first; reads only the archive."`
	Search  scoutSearchCmd  `cmd:"" help:"Search the messages of archived Scout sessions; reads only the archive."`
	Show    scoutShowCmd    `cmd:"" help:"Show one scout-session:ID with its messages; reads only the archive."`
	Sources scoutSourcesCmd `cmd:"" help:"Show each Scout folder read, its last scan and its file counts, including cloud-only files that were skipped."`
}

// scoutAboutHelp says what the source is, that it is opt-in and private, and how to read it.
const scoutAboutHelp = "Microsoft Scout keeps a JSON copy of each session (one conversation, or one automation run) in your own OneDrive, one folder per device. m365crawl reads those files only when you run `scout sync`: it is off until then, and `sync`, `watch` and `search` never read them. " +
	"Afterwards `scout list`, `scout search WORDS` and `scout show scout-session:N` read the archive offline, and `scout sources` shows what each scan read and skipped.\n" +
	"These files are private AI conversation history: everything the agent saw and did, including tool output that can quote mail, chats and files. m365crawl handles them like your private chats: they stay in the local archive, are never uploaded, and are never written to logs. Before anything is stored, secrets are replaced with [redacted]: the Teams rules (JWTs, strings that start with Bearer, credential-named keys, sig= parameters) and Scout rules for tool output (Bearer tokens anywhere, GitHub, OpenAI-style sk-, Slack, AWS and npm tokens, private key blocks, connection-string keys and passwords, token-like URL parameters, NAME_TOKEN/SECRET/PASSWORD/API_KEY assignments, Cookie headers, values under JSON keys that name a secret). When those rules or the parser change, the next sync or scout sync scrubs and maps every archived session again from the archive alone, gone ones included. Secrets of other shapes are kept as written.\n" +
	"The session file format is not a documented interface and can change between Scout releases, so the source is best-effort (scout_coverage.format is \"unofficial\"). A file that cannot be read is counted, never guessed.\n"

// scoutResidencyHelp says why a session can be missing and what fixes it.
const scoutResidencyHelp = "OneDrive can keep a file only in the cloud (Files On-Demand). m365crawl never downloads such a file: `scout sync` counts it as cloud_only and skips it, so a missing session is not evidence that the agent did nothing. " +
	"To include those sessions, right-click the Scout app folder in your OneDrive's Apps folder, choose \"Always keep on this device\", wait for OneDrive to download it, then run `scout sync` again.\n" +
	"The archive keeps every version it read. When Scout or you delete a session, its file leaves OneDrive and the archived copy stays (file_state gone).\n"

func (scoutGroup) Help() string {
	return scoutAboutHelp + scoutResidencyHelp +
		"Acquisition is supported on macOS, which reads ~/Library/CloudStorage/OneDrive-*/Apps/*/ and keeps any app folder with a sessions/devices folder. On Linux, name the folder with --root. Windows acquisition is refused (scout_platform_unqualified) until it is verified on a Windows computer; reading an archive works everywhere.\n" +
		"Private Microsoft 365 Copilot chat history and Copilot Cowork history are not available: Microsoft's only export of Copilot interactions works for a whole organization with administrator consent, not for one signed-in user."
}

type scoutSessionItem struct {
	ID                string         `json:"id"`
	SourceID          int64          `json:"source_id"`
	ObservationID     int64          `json:"observation_id"`
	Observations      int            `json:"observations"`
	LatestObservation bool           `json:"latest_observation"`
	SessionID         string         `json:"session_id"`
	Name              string         `json:"name"`
	NameTruncated     bool           `json:"name_truncated,omitempty"`
	DeviceID          string         `json:"device_id"`
	Origin            *string        `json:"origin"`
	Automation        bool           `json:"automation"`
	Model             string         `json:"model"`
	CreatedAt         *time.Time     `json:"created_at"`
	UpdatedAt         *time.Time     `json:"updated_at"`
	FirstMessageAt    *time.Time     `json:"first_message_at"`
	LastMessageAt     *time.Time     `json:"last_message_at"`
	MessageCount      int            `json:"message_count"`
	FileState         string         `json:"file_state"`
	FileReason        string         `json:"file_reason,omitempty"`
	GoneAt            *time.Time     `json:"gone_at"`
	FileModifiedAt    time.Time      `json:"file_modified_at"`
	CapturedAt        time.Time      `json:"captured_at"`
	Digest            string         `json:"digest"`
	Redactions        int            `json:"redactions"`
	Losses            map[string]int `json:"losses"`
}

type scoutMessageItem struct {
	ID            string     `json:"id"`
	Session       string     `json:"session"`
	ObservationID int64      `json:"observation_id"`
	Ordinal       int        `json:"ordinal"`
	SessionName   string     `json:"session_name"`
	DeviceID      string     `json:"device_id"`
	Automation    bool       `json:"automation"`
	Role          string     `json:"role"`
	At            *time.Time `json:"at"`
	Text          string     `json:"text"`
	TextTruncated bool       `json:"text_truncated,omitempty"`
	ToolCalls     int        `json:"tool_calls"`
	FileNames     int        `json:"file_names"`
	Reasoning     bool       `json:"reasoning"`
}

type scoutShowMessage struct {
	ID            string     `json:"id"`
	Ordinal       int        `json:"ordinal"`
	Role          string     `json:"role"`
	At            *time.Time `json:"at"`
	Text          string     `json:"text"`
	TextTruncated bool       `json:"text_truncated,omitempty"`
	ToolCalls     int        `json:"tool_calls"`
	FileNames     int        `json:"file_names"`
	Reasoning     bool       `json:"reasoning"`
}

// scoutDetailItem is the one item scout show returns: the session and a page of its messages.
type scoutDetailItem struct {
	scoutSessionItem
	MessagesOffset    int                `json:"messages_offset"`
	MessagesTruncated bool               `json:"messages_truncated"`
	Messages          []scoutShowMessage `json:"messages"`
}

type scoutSourceItem struct {
	SourceID          int64      `json:"source_id"`
	Platform          string     `json:"platform"`
	LocatorTail       string     `json:"locator_tail"`
	RegisteredAt      time.Time  `json:"registered_at"`
	LastScanAt        *time.Time `json:"last_scan_at"`
	LastOutcome       string     `json:"last_outcome"`
	LastReason        string     `json:"last_reason,omitempty"`
	Fix               string     `json:"fix,omitempty"`
	Devices           int        `json:"devices"`
	UnreadableDevices int        `json:"unreadable_devices"`
	PresentFiles      int        `json:"present_files"`
	ArchivedFiles     int        `json:"archived_files"`
	CloudOnlyFiles    int        `json:"cloud_only_files"`
	FailedFiles       int        `json:"failed_files"`
	GoneFiles         int        `json:"gone_files"`
	Sessions          int        `json:"sessions"`
}

type scoutTotals struct {
	Present   int `json:"present_files"`
	Archived  int `json:"archived_files"`
	CloudOnly int `json:"cloud_only_files"`
	Failed    int `json:"failed_files"`
	Gone      int `json:"gone_files"`
	Sessions  int `json:"sessions"`
}

// scoutCoverage describes every registered Scout folder beside a Scout result, outside --fields,
// clipping and zero hits, so an empty or short answer is never read as "the agent did nothing".
type scoutCoverage struct {
	State   string            `json:"state"`
	Format  string            `json:"format"`
	Totals  scoutTotals       `json:"totals"`
	Sources []scoutSourceItem `json:"sources"`
	Note    string            `json:"note,omitempty"`
}

// scoutReasons give each fixed reason its remedy.
var scoutReasons = map[string]string{
	"scout_root_invalid":         "Pass the Scout app folder itself: the folder inside your OneDrive's Apps folder that holds sessions/devices.",
	"scout_root_unqualified":     "Use a folder on a local disk (APFS on macOS, a local filesystem on Linux); network and FUSE filesystems can download when read.",
	"scout_devices_unreadable":   "The sessions/devices folder could not be listed, possibly because OneDrive keeps it only in the cloud: set the app folder to \"Always keep on this device\" and run m365crawl scout sync again.",
	"scout_guard_unavailable":    "This macOS could not turn off downloads of cloud-only files for the read, so nothing was read; report it at " + issuesURL + ".",
	"scout_guard_restore_failed": "The read finished but the download policy could not be restored on one thread; run m365crawl scout sync again and report it at " + issuesURL + ".",
	"scout_capture_failed":       "The scan stopped unexpectedly; run m365crawl scout sync again.",
}

func scoutFix(reason string, cloudOnly int) string {
	if fix, ok := scoutReasons[reason]; ok {
		return fix
	}
	if cloudOnly > 0 {
		return scoutCloudOnlyFix
	}
	return ""
}

const scoutCloudOnlyFix = "Some session files are kept only in the cloud and were not read: set the Scout app folder to \"Always keep on this device\" in OneDrive, wait for the download, then run m365crawl scout sync again."

func scoutSourceItemOf(src store.ScoutSource) scoutSourceItem {
	item := scoutSourceItem{SourceID: src.ID, Platform: src.Platform, LocatorTail: locatorTail(src.Root), RegisteredAt: src.RegisteredAt,
		LastOutcome: "pending", Devices: src.Devices, PresentFiles: src.Files.Present, ArchivedFiles: src.Files.Archived,
		CloudOnlyFiles: src.Files.CloudOnly, FailedFiles: src.Files.Failed, GoneFiles: src.Files.Gone, Sessions: src.Files.Sessions}
	if run := src.LastRun; run != nil {
		item.LastScanAt, item.LastOutcome, item.LastReason, item.UnreadableDevices = run.FinishedAt, run.Outcome, run.Reason, run.UnreadableDevices
	}
	item.Fix = scoutFix(item.LastReason, item.CloudOnlyFiles)
	return item
}

func scoutCoverageOf(sources []store.ScoutSource) *scoutCoverage {
	out := &scoutCoverage{State: "not_registered", Format: "unofficial", Sources: []scoutSourceItem{}}
	for _, src := range sources {
		item := scoutSourceItemOf(src)
		out.Sources = append(out.Sources, item)
		out.Totals.Present += item.PresentFiles
		out.Totals.Archived += item.ArchivedFiles
		out.Totals.CloudOnly += item.CloudOnlyFiles
		out.Totals.Failed += item.FailedFiles
		out.Totals.Gone += item.GoneFiles
		out.Totals.Sessions += item.Sessions
	}
	switch {
	case len(sources) == 0:
		out.Note = "no Scout folder has been read; run m365crawl scout sync to opt in"
	default:
		out.State = "registered"
		if out.Totals.CloudOnly > 0 {
			out.Note = plural(out.Totals.CloudOnly, "session file is", "session files are") + " kept only in the cloud and not archived, so sessions can be missing: " + scoutCloudOnlyFix
		}
	}
	return out
}

func scoutSessionItemOf(row store.ScoutSession, maxText int) scoutSessionItem {
	o := row.Observation
	name, cut := truncateRunes(o.Name, maxText)
	item := scoutSessionItem{ID: fmt.Sprintf("scout-session:%d", row.FileID), SourceID: row.SourceID, ObservationID: o.ID, Observations: row.Observations,
		LatestObservation: o.ID == row.LatestObservation, SessionID: o.NativeID, Name: name, NameTruncated: cut, DeviceID: row.Device,
		Automation: o.Automation, Model: o.Model, CreatedAt: utcPointer(o.CreatedAt), UpdatedAt: utcPointer(o.UpdatedAt),
		FirstMessageAt: utcPointer(o.FirstMessageAt), LastMessageAt: utcPointer(o.LastMessageAt), MessageCount: o.MessageCount,
		FileState: row.State, FileReason: row.Reason, GoneAt: utcPointer(row.GoneAt), FileModifiedAt: o.FileModifiedAt.UTC(),
		CapturedAt: o.CapturedAt.UTC(), Digest: o.Digest, Redactions: o.Redactions, Losses: o.Losses}
	if o.Origin != "" {
		origin := o.Origin
		item.Origin = &origin
	}
	if row.GoneAt != nil {
		item.FileState = "gone"
	}
	if item.Losses == nil {
		item.Losses = map[string]int{}
	}
	return item
}

func (rt *runtime) scoutFlags() error {
	if rt.g.Account != "" || rt.g.OutlookAccount != "" || rt.g.OutlookProfile != "" {
		return errs.Usage("Scout sessions belong to no Teams account or Outlook profile; drop --account, --outlook-account and --outlook-profile")
	}
	return nil
}

func scoutError(err error) error {
	var capture *scoutcapture.Error
	switch {
	case errors.Is(err, store.ErrScoutSessionNotFound):
		return &errs.Coded{Code: "scout_session_unknown", Exit: errs.ExitUsage, Message: "Scout session or observation not found",
			Fix: "Run `m365crawl scout list` for archived scout-session:IDs; --observation takes an observation_id of that same session."}
	case errors.Is(err, scoutcapture.ErrPlatformUnqualified):
		return &errs.Coded{Code: "scout_platform_unqualified", Exit: errs.ExitEnvironment,
			Message: "reading Scout session files without downloading cloud-only ones is not verified on this platform, so nothing was read",
			Fix:     "Run scout sync on macOS, or on Linux with --root; archived sessions can still be read here with scout list, search and show."}
	case errors.Is(err, syncer.ErrScoutFolderNotFound):
		return &errs.Coded{Code: "scout_folder_not_found", Exit: errs.ExitEnvironment,
			Message: "no Scout session folder was found in a local OneDrive folder",
			Fix:     "Sign in to OneDrive and to Scout so Scout saves its sessions there, or pass the app folder that holds sessions/devices with --root DIR."}
	case errors.As(err, &capture):
		return &errs.Coded{Code: capture.Code, Exit: errs.ExitEnvironment, Message: "the Scout folder could not be read", Fix: scoutReasons[capture.Code]}
	}
	return asCoded(err)
}

// scoutRead opens the archive read-only and runs fn; a missing archive reads as empty. It never
// reads a Scout folder, syncs or migrates.
func (rt *runtime) scoutRead(label string, fn func(*store.Store) (*listResult, []store.ScoutSource, error)) error {
	if err := rt.scoutFlags(); err != nil {
		return err
	}
	var result *listResult
	var sources []store.ScoutSource
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	switch {
	case errors.Is(err, store.ErrNoArchive) && label == "scout show":
		return scoutError(store.ErrScoutSessionNotFound)
	case errors.Is(err, store.ErrNoArchive):
		result = newList(nil, false)
	case err != nil:
		return scoutError(err)
	default:
		var readErr error
		result, sources, readErr = fn(st)
		if err := errors.Join(readErr, st.Close()); err != nil {
			return scoutError(err)
		}
	}
	result.ScoutCoverage = scoutCoverageOf(sources)
	result.sourceOnly = true
	result.ArchiveAgeSeconds = rt.scoutAge(sources)
	if coverage := result.ScoutCoverage; result.Count == 0 && result.Note == "" {
		switch {
		case coverage.State == "not_registered":
			result.Note = coverage.Note
		case coverage.Totals.CloudOnly > 0:
			result.Note = "nothing matched; " + coverage.Note
		default:
			result.Note = "nothing matched in the archived Scout sessions; scout_coverage says what each folder's last scan read and skipped"
		}
	}
	return rt.write(label, result)
}

// scoutAge is archive_age_seconds of a Scout result: the seconds since the newest finished scan.
func (rt *runtime) scoutAge(sources []store.ScoutSource) *int64 {
	var newest time.Time
	for _, src := range sources {
		if src.LastRun != nil && src.LastRun.FinishedAt != nil && src.LastRun.FinishedAt.After(newest) {
			newest = *src.LastRun.FinishedAt
		}
	}
	if newest.IsZero() {
		return nil
	}
	age := max(int64(rt.now().Sub(newest).Seconds()), 0)
	return &age
}

type scoutFolderItem struct {
	SourceID          int64  `json:"source_id"`
	LocatorTail       string `json:"locator_tail"`
	Outcome           string `json:"outcome"`
	Reason            string `json:"reason,omitempty"`
	Fix               string `json:"fix,omitempty"`
	Devices           int    `json:"devices"`
	UnreadableDevices int    `json:"unreadable_devices"`
	Listed            int    `json:"listed"`
	Admitted          int    `json:"admitted"`
	Unchanged         int    `json:"unchanged"`
	Rescrubbed        int    `json:"rescrubbed"`
	CloudOnly         int    `json:"cloud_only"`
	Unsafe            int    `json:"unsafe"`
	TooLarge          int    `json:"too_large"`
	Failed            int    `json:"failed"`
	Gone              int    `json:"gone"`
	Redacted          int    `json:"redacted"`
}

type scoutSyncResult struct {
	Discovered bool              `json:"discovered"`
	Folders    []scoutFolderItem `json:"folders"`
	Admitted   int               `json:"admitted"`
	Unchanged  int               `json:"unchanged"`
	Rescrubbed int               `json:"rescrubbed"`
	// RescrubUnparseable counts re-scrubbed versions that no longer mapped and were blanked.
	RescrubUnparseable int            `json:"rescrub_unparseable"`
	CloudOnly          int            `json:"cloud_only"`
	Failed             int            `json:"failed"`
	Gone               int            `json:"gone"`
	Notices            []string       `json:"notices,omitempty"`
	ScoutCoverage      *scoutCoverage `json:"scout_coverage"`
}

var (
	runScoutSync = syncer.SyncScout
	scoutHome    = os.UserHomeDir
)

type scoutSyncCmd struct {
	Root []string `name:"root" sep:"none" help:"A Scout app folder to read (the folder that holds sessions/devices); repeatable. Default: every such folder under the Apps folder of each local OneDrive folder (macOS)." placeholder:"DIR"`
}

func (scoutSyncCmd) Help() string {
	return scoutAboutHelp + scoutResidencyHelp +
		"A file is opened only when it is resident and its size or modification time changed since the last read; its bytes are digested, and new bytes become a new immutable observation while bytes seen before reuse their observation. " +
		"Each folder's outcome is complete (every listed session is archived as it is now), partial (some file or device folder was skipped: see cloud_only, unsafe, too_large, failed and unreadable_devices), failed (reason and fix say why) or cancelled. The command exits 0 when at least one folder was scanned."
}

func (c *scoutSyncCmd) Run(rt *runtime) error {
	if err := rt.scoutFlags(); err != nil {
		return err
	}
	home, err := scoutHome()
	if err != nil && len(c.Root) == 0 {
		return &errs.Coded{Code: "scout_folder_not_found", Exit: errs.ExitEnvironment, Message: "the home folder is unknown, so no OneDrive folder can be found",
			Fix: "Pass the Scout app folder with --root DIR."}
	}
	report, runErr := runScoutSync(rt.ctx, syncer.ScoutOptions{DBPath: rt.dbPath, Roots: c.Root, Home: home})
	result := scoutSyncResult{Discovered: report.Discovered, Rescrubbed: report.Rescrubbed, RescrubUnparseable: report.RescrubUnparseable,
		Folders: []scoutFolderItem{}}
	scanned := false
	for _, f := range report.Folders {
		item := scoutFolderItem{SourceID: f.SourceID, LocatorTail: locatorTail(f.Root), Outcome: f.Outcome, Reason: f.Reason, Fix: scoutFix(f.Reason, f.CloudOnly),
			Devices: f.Devices, UnreadableDevices: f.UnreadableDevices, Listed: f.Listed, Admitted: f.Admitted, Unchanged: f.Unchanged,
			Rescrubbed: f.Rescrubbed, CloudOnly: f.CloudOnly, Unsafe: f.Unsafe, TooLarge: f.TooLarge, Failed: f.Failed, Gone: f.Gone, Redacted: f.Redacted}
		result.Folders = append(result.Folders, item)
		result.Admitted += f.Admitted
		result.Unchanged += f.Unchanged
		result.Rescrubbed += f.Rescrubbed
		result.CloudOnly += f.CloudOnly
		result.Failed += f.Failed + f.Unsafe + f.TooLarge
		result.Gone += f.Gone
		scanned = scanned || f.Outcome != "failed"
	}
	if result.Rescrubbed > 0 {
		result.Notices = append(result.Notices, plural(result.Rescrubbed, "archived session version was", "archived session versions were")+
			" stored under older scrub or parser rules and were scrubbed and mapped again in place, gone sessions included")
	}
	if result.RescrubUnparseable > 0 {
		result.Notices = append(result.Notices, plural(result.RescrubUnparseable, "archived session version", "archived session versions")+
			" no longer mapped after scrubbing and kept nothing but its file record (loss rescrub_unparseable); read the session again from OneDrive to restore it")
	}
	if result.CloudOnly > 0 {
		result.Notices = append(result.Notices, plural(result.CloudOnly, "session file is", "session files are")+
			" kept only in the cloud and was not read; m365crawl never downloads one. "+scoutCloudOnlyFix)
	}
	if result.Gone > 0 {
		result.Notices = append(result.Notices, plural(result.Gone, "session file is", "session files are")+
			" no longer in OneDrive; the archive keeps what it read (file_state gone)")
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err == nil {
		sources, _, readErr := st.ScoutSources(rt.ctx)
		err = errors.Join(readErr, st.Close())
		result.ScoutCoverage = scoutCoverageOf(sources)
	}
	if runErr == nil && err != nil && !errors.Is(err, store.ErrNoArchive) {
		runErr = err
	}
	if result.ScoutCoverage == nil {
		result.ScoutCoverage = scoutCoverageOf(nil)
	}
	if runErr != nil {
		if len(report.Folders) > 0 || report.Rescrubbed > 0 {
			_ = rt.write("scout sync", result) // the run's own error is the one to return
		}
		return scoutError(runErr)
	}
	if !scanned && len(result.Folders) > 0 {
		_ = rt.write("scout sync", result)
		first := result.Folders[0]
		exit := errs.ExitEnvironment
		if first.Reason == "scout_root_invalid" {
			exit = errs.ExitUsage
		}
		return &errs.Coded{Code: first.Reason, Exit: exit, Message: "no Scout folder could be scanned", Fix: first.Fix}
	}
	return rt.write("scout sync", result)
}

// scoutDetailKeys are the --fields keys of scout show: the session's and the message page's.
func scoutDetailKeys() []string {
	return append(typeKeys[scoutSessionItem](), "messages_offset", "messages_truncated", "messages")
}

const scoutListHelp = "Each row is one session file with the newest version the archive read (observation_id). id scout-session:N is stable across versions; observations counts the versions archived. " +
	"Activity is the last message time, else the session's own updated time, else the file's modification time; rows are newest activity first, then newest ID. " +
	"--since keeps sessions whose activity reaches it and --until those that began before it (a date alone as --until includes that whole local day; a time is exclusive). " +
	"origin is the file's own source label (automation, teams and so on), null for a conversation you started. file_state is archived, cloud_only (archived earlier, kept only in the cloud now), failed, unsafe, too_large or gone (no longer in OneDrive; the archive keeps it). " +
	"Times are the file's own claims; captured_at is when m365crawl read it.\n"

type scoutFilterFlags struct {
	SourceID int64  `name:"source-id" help:"Only this Scout folder; scout sources lists IDs."`
	Device   string `help:"Only this device folder (device_id)." placeholder:"ID"`
	Origin   string `default:"all" enum:"all,automation,interactive" help:"all, automation (runs an automation started) or interactive (the rest)."`
	Since    string `help:"Only activity at or after this time (YYYY-MM-DD, RFC3339 or an age such as 7d)." placeholder:"DATE"`
	Until    string `help:"Only activity before this time; a date alone includes that whole local day, a time is exclusive." placeholder:"DATE"`
	Limit    int    `default:"50" help:"Maximum rows; 0 returns all. Check truncated."`
}

func (f scoutFilterFlags) filter(rt *runtime) (store.ScoutFilter, error) {
	out := store.ScoutFilter{SourceID: f.SourceID, Device: f.Device, Limit: f.Limit}
	if f.SourceID < 0 || f.Limit < 0 {
		return out, errs.Usage("--source-id and --limit must not be negative")
	}
	since, until, err := rt.window("--since", f.Since, "--until", f.Until)
	if err != nil {
		return out, err
	}
	out.Since, out.Until = since, until
	if f.Origin != "all" {
		automation := f.Origin == "automation"
		out.Automation = &automation
	}
	return out, nil
}

type scoutListCmd struct {
	scoutFilterFlags `embed:""`
}

func (scoutListCmd) Help() string { return scoutListHelp + scoutResidencyHelp }

func (c *scoutListCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "scout list", ""); err != nil {
		return err
	}
	filter, err := c.filter(rt)
	if err != nil {
		return err
	}
	return rt.scoutRead("scout list", func(st *store.Store) (*listResult, []store.ScoutSource, error) {
		rows, truncated, err := st.ScoutSessions(rt.ctx, filter)
		if err != nil {
			return nil, nil, err
		}
		sources, _, err := st.ScoutSources(rt.ctx)
		if err != nil {
			return nil, nil, err
		}
		out := make([]scoutSessionItem, len(rows))
		for i, row := range rows {
			out[i] = scoutSessionItemOf(row, rt.g.MaxText)
		}
		return newList(shape(rt, out), truncated), sources, nil
	})
}

type scoutSearchCmd struct {
	Query            string `arg:"" help:"Words in message text or the session name; required."`
	scoutFilterFlags `embed:""`
}

func (scoutSearchCmd) Help() string {
	return "Each row is one matching message (scout-message:N) with the session it belongs to (scout-session:N, follow it with scout show). The session name is searched with every message. " +
		"A message kept unchanged by several archived versions of a session is returned once, from the newest. Messages with a known time come first, newest first; --since and --until compare the message time and leave out messages without one.\n" +
		scoutResidencyHelp
}

func (c *scoutSearchCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "scout search", ""); err != nil {
		return err
	}
	if strings.TrimSpace(c.Query) == "" {
		return errs.Usage("scout search needs words; use scout list for an unfiltered list")
	}
	filter, err := c.filter(rt)
	if err != nil {
		return err
	}
	return rt.scoutRead("scout search", func(st *store.Store) (*listResult, []store.ScoutSource, error) {
		hits, truncated, err := st.ScoutSearch(rt.ctx, c.Query, filter)
		if err != nil {
			return nil, nil, err
		}
		sources, _, err := st.ScoutSources(rt.ctx)
		if err != nil {
			return nil, nil, err
		}
		out := make([]scoutMessageItem, len(hits))
		for i, hit := range hits {
			m := hit.Message
			text, cut := truncateRunes(m.Text, rt.g.MaxText)
			name, _ := truncateRunes(hit.Session.Observation.Name, rt.g.MaxText)
			out[i] = scoutMessageItem{ID: fmt.Sprintf("scout-message:%d", m.ID), Session: fmt.Sprintf("scout-session:%d", hit.Session.FileID),
				ObservationID: hit.Session.Observation.ID, Ordinal: m.Ordinal, SessionName: name, DeviceID: hit.Session.Device,
				Automation: hit.Session.Observation.Automation, Role: m.Role, At: utcPointer(m.At), Text: text, TextTruncated: cut,
				ToolCalls: m.ToolCalls, FileNames: m.FileNames, Reasoning: m.Reasoning}
		}
		return newList(shape(rt, out), truncated), sources, nil
	})
}

type scoutShowCmd struct {
	ID          string `arg:"" help:"scout-session:ID from scout list or scout search."`
	Observation int64  `help:"Show this archived version (an observation_id of the session) instead of the newest."`
	Offset      int    `help:"Skip this many messages first."`
	Limit       int    `default:"100" help:"Maximum messages; 0 returns all. messages_truncated says whether more follow."`
}

func (scoutShowCmd) Help() string {
	return "scout show returns one item: the session, as in scout list, with its messages in file order from --offset. --max-text clips each message's text and sets text_truncated. " +
		"role is the message's own role (system, user or assistant); tool_calls and file_names count what the message lists, and reasoning says it carries reasoning text, which is not shown."
}

func (c *scoutShowCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "scout show", ""); err != nil {
		return err
	}
	raw, prefixed := strings.CutPrefix(c.ID, "scout-session:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !prefixed || err != nil || id <= 0 || raw != strconv.FormatInt(id, 10) {
		return errs.Usage("a Scout session ID is scout-session: followed by a positive decimal ID; use scout list")
	}
	if c.Observation < 0 || c.Offset < 0 || c.Limit < 0 {
		return errs.Usage("--observation, --offset and --limit must not be negative")
	}
	return rt.scoutRead("scout show", func(st *store.Store) (*listResult, []store.ScoutSource, error) {
		detail, err := st.ScoutSessionDetail(rt.ctx, id, c.Observation, c.Offset, c.Limit)
		if err != nil {
			return nil, nil, err
		}
		sources, _, err := st.ScoutSources(rt.ctx)
		if err != nil {
			return nil, nil, err
		}
		item := scoutDetailItem{scoutSessionItem: scoutSessionItemOf(detail.Session, rt.g.MaxText), MessagesOffset: c.Offset,
			MessagesTruncated: c.Offset+len(detail.Messages) < detail.MessagesTotal, Messages: []scoutShowMessage{}}
		for _, m := range detail.Messages {
			text, cut := truncateRunes(m.Text, rt.g.MaxText)
			item.Messages = append(item.Messages, scoutShowMessage{ID: fmt.Sprintf("scout-message:%d", m.ID), Ordinal: m.Ordinal, Role: m.Role,
				At: utcPointer(m.At), Text: text, TextTruncated: cut, ToolCalls: m.ToolCalls, FileNames: m.FileNames, Reasoning: m.Reasoning})
		}
		return newList(shape(rt, []scoutDetailItem{item}), false), sources, nil
	})
}

type scoutSourcesCmd struct{}

func (scoutSourcesCmd) Help() string {
	return "One row per Scout folder scout sync has read: its last scan's outcome, reason and fix, and its files by state at that scan. present_files excludes gone files; sessions counts files the archive holds a read of, gone ones included. locator_tail is the folder's last two path elements. Reads only the archive.\n" +
		scoutResidencyHelp
}

func (c *scoutSourcesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "scout sources", ""); err != nil {
		return err
	}
	return rt.scoutRead("scout sources", func(st *store.Store) (*listResult, []store.ScoutSource, error) {
		sources, _, err := st.ScoutSources(rt.ctx)
		if err != nil {
			return nil, nil, err
		}
		items := make([]scoutSourceItem, len(sources))
		for i, src := range sources {
			items[i] = scoutSourceItemOf(src)
		}
		return newList(shape(rt, items), false), sources, nil
	})
}

// warnScoutRescrub reports a sync whose Scout re-scrub stopped: the sync itself went on.
func (rt *runtime) warnScoutRescrub(rep syncer.Report) {
	if e := rep.ScoutRescrubError; e != nil {
		rt.printWarning(&errs.Coded{Code: e.Code, Exit: errs.ExitRuntime, Message: e.Message, Fix: e.Fix})
	}
}

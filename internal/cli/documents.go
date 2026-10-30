package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/officeregistry"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

type documentsGroup struct {
	Sync    documentsSyncCmd    `cmd:"" help:"Register and capture ordinary local Office JSON, Office registry and OneDrive usage metadata; never opens a referenced document."`
	List    documentsListCmd    `cmd:"" help:"List document observations with stable IDs and source coverage."`
	Search  documentsSearchCmd  `cmd:"" help:"Search retained document metadata, native URLs and sharing/person claims."`
	Show    documentsShowCmd    `cmd:"" help:"Inspect one document observation, raw native claims and unresolved URL references."`
	Sources documentsSourcesCmd `cmd:"" help:"Inspect registered document source health without capture."`
}

func (documentsGroup) Help() string {
	return "Documents are retained Office recent/shared/recommended/dialog metadata observations, Office registry recently-opened entries and OneDrive usage and sharing records, not original document text, resolved identities or a complete library export.\n" +
		"Register once with `docs sync` for named Mac Office caches, the Mac Office registry and the Mac OneDrive usage files, `docs sync --root DIR` for ordinary local recent.json/shared.json/recommended.json/dialog.json copies, `docs sync --registry-file FILE` for one ordinary local copy of an Office registry file, or `docs sync --onedrive-usage-file FILE` for one ordinary local copy of a OneDrive usage file. Windows accepts qualified explicit ordinary NTFS inputs, not default discovery. Unknown/provider/reparse/dataless inputs are refused; no document URL or friendly path is opened, no hydration or network fallback.\n" +
		documentRegistryHelp + "\n" + documentUsageHelp + "\n" +
		"Then use `docs list --query WORDS --limit 10`, `docs search WORDS`, or `search WORDS --source documents`. Follow a returned document-observation:ID with `docs show document-observation:ID`; `docs sources` lists source IDs and coverage. Ordinary reads refresh only registered sources; --no-sync is archive-only, and docs sources never captures. A read copies a registry or OneDrive usage file again only when its size or modification time differs from the last capture that finished, or when the last attempt failed and is older than --max-age; otherwise the source's attempted_at stays at that capture, however old. `docs sync` always captures. Partial/failed inputs keep last-good observations. A successful Office response that lists no documents is complete with reason office_response_listed_no_documents (earlier rows become retained); it does not mean the account has none.\n" +
		documentTerms() +
		"--since/--until use native document modification time, not opened/shared/file/capture time; unknown dates do not match bounded windows. Native sharing/activity are source claims, not counted new events. Account/author/chat/mail filters cannot qualify these unresolved documents. Text state is not_captured; there is no docs read/text command."
}

// documentSurfaces says what each surface is, for docs --help and docs show.
var documentSurfaces = map[officedocuments.Surface]string{
	officedocuments.Dialog:      "the app's local Open-dialog recent list, often file:// paths",
	officedocuments.Recent:      "Office's Recent list of files the account opened, from Office's service cache",
	officedocuments.Recommended: "Office's Recommended list of files its service suggests, with its activity badge",
	officedocuments.Shared:      "Office's Shared with me list, with the sharer and date Office reported",
}

// documentRegistryHelp says what a registry row is and how it differs from an Office JSON row.
const documentRegistryHelp = "Registry rows (variant office_registry, application registry, no surface) are entries of Office's local recently-opened list, kept in registry_native exactly as the registry file states them: title, URL, friendly path, the Office app the entry sits under, size and pin state. " +
	"opened_at is the registry's own opened time as UTC, or null when the entry has none or it is not an RFC 3339 time with an explicit offset; registry_native.OpenedRaw keeps the exact text. " +
	"The registry states no modification time, so modified_at is always null for these rows and --since/--until never match them. " +
	"They are metadata only: the document itself, its URL and its friendly path are never opened, checked or resolved."

const (
	documentAssociationHelp = "m365crawl has not linked the row to a file, owner, account, chat or mail; URLs are evidence only"
	documentClocksHelp      = "modified_at is the native document modification time the source reported (null when it gave none or it did not parse; Raw keeps the text); captured_at is when m365crawl first copied the file holding this exact row; source_observed_at is that file's modification time on disk when copied"
	documentOrderHelp       = "Order: modified_at newest first, null modified_at last, then title (ASCII case-insensitive), source ID, a usage row's native Type, ID and Format, and observation ID."
)

// documentTerms defines the surfaces, unresolved states, Sharing.Type, clocks and order for docs --help.
func documentTerms() string {
	surfaces := []string{}
	for _, surface := range []officedocuments.Surface{officedocuments.Dialog, officedocuments.Recent, officedocuments.Recommended, officedocuments.Shared} {
		surfaces = append(surfaces, string(surface)+": "+documentSurfaces[surface])
	}
	return "Surfaces: " + strings.Join(surfaces, "; ") + ". Application supplied means a file given with --root, so the Office app is unknown; application registry means an Office registry row, whose own app claim is registry_native.ApplicationPath; application onedrive means a OneDrive usage row.\n" +
		"association_state unresolved: " + documentAssociationHelp + ". boundary unresolved (docs sources): a source file is not proven to belong to one account. " +
		"native Sharing.Type is Office's SharingType number as written, kept unmapped: m365crawl does not know its meanings.\n" +
		"Clocks: " + documentClocksHelp + ". " + documentOrderHelp + "\n"
}

type documentItem struct {
	ID               string                  `json:"id"`
	SourceID         int64                   `json:"source_id"`
	CaptureID        int64                   `json:"capture_id"`
	Application      string                  `json:"application"`
	Surface          officedocuments.Surface `json:"surface"`
	Title            string                  `json:"title"`
	URL              string                  `json:"url"`
	WebURL           string                  `json:"web_url"`
	Native           *documentNative         `json:"native,omitempty"`
	ModifiedAt       *time.Time              `json:"modified_at"`
	CapturedAt       *time.Time              `json:"captured_at"`
	SourceObservedAt *time.Time              `json:"source_observed_at"`
	Retained         bool                    `json:"retained"`
	AssociationState string                  `json:"association_state"`
	TextState        string                  `json:"text_state"`
	TextTruncated    bool                    `json:"text_truncated,omitempty"`
	// Variant is present on Office registry and OneDrive usage rows, RegistryNative and OpenedAt
	// only on registry rows, UsageNative and FullInspection only on usage rows, so an Office JSON
	// row keeps exactly the keys it always had.
	Variant        string                      `json:"variant,omitempty"`
	RegistryNative *officeregistry.Observation `json:"registry_native,omitempty"`
	OpenedAt       *documentOpened             `json:"opened_at,omitempty"`
	UsageNative    *usageNativeItem            `json:"usage_native,omitempty"`
	FullInspection string                      `json:"full_inspection,omitempty"`
}

// documentOpened is a registry row's opened time. It always encodes, as a UTC time or as null,
// so an unparsed registry clock is a visible null rather than a missing key.
type documentOpened struct{ At *time.Time }

func (o documentOpened) MarshalJSON() ([]byte, error) { return json.Marshal(o.At) }

// registryItemOf is the item of an Office registry row: its native claims with the descriptive
// strings clipped to maxText, and no Office JSON claims.
func registryItemOf(row store.DocumentObservationRow, maxText int) documentItem {
	native := *row.Registry
	truncated := false
	for _, field := range []*string{&native.Title, &native.FriendlyPath, &native.ApplicationPath, &native.ApplicationValue} {
		var cut bool
		*field, cut = truncateRunes(*field, maxText)
		truncated = truncated || cut
	}
	return documentItem{ID: fmt.Sprintf("document-observation:%d", row.ID), SourceID: row.SourceID,
		CaptureID: row.CaptureID, Application: row.Application, Title: native.Title, URL: native.URL,
		CapturedAt: row.CapturedAt, SourceObservedAt: row.SourceObservedAt, Retained: row.Retained,
		AssociationState: "unresolved", TextState: "not_captured", TextTruncated: truncated,
		Variant: row.Variant, RegistryNative: &native, OpenedAt: &documentOpened{At: row.OpenedAt}}
}

// documentItemOf builds a row's item. db is the archive path a usage row's full_inspection names.
func documentItemOf(row store.DocumentObservationRow, maxText int, db string) documentItem {
	if row.Registry != nil {
		return registryItemOf(row, maxText)
	}
	if row.Usage != nil {
		return usageItemOf(row, maxText, db)
	}
	native := row.Document
	title, truncated := truncateRunes(native.Title, maxText)
	native.Title = title
	var cut bool
	for _, field := range []*string{&native.FriendlyPath, &native.SiteTitle, &native.TeamsChannelTitle} {
		*field, cut = truncateRunes(*field, maxText)
		truncated = truncated || cut
	}
	for _, person := range []*officedocuments.Person{&native.Creator, &native.Modifier} {
		person.DisplayName, cut = truncateRunes(person.DisplayName, maxText)
		truncated = truncated || cut
	}
	if native.Sharing != nil {
		sharing := *native.Sharing
		sharing.DisplayName, cut = truncateRunes(sharing.DisplayName, maxText)
		truncated = truncated || cut
		native.Sharing = &sharing
	}
	if native.Activity != nil {
		activity := *native.Activity
		activity.MessageFormat, cut = truncateRunes(activity.MessageFormat, maxText)
		truncated = truncated || cut
		activity.Users = append([]officedocuments.Person{}, activity.Users...)
		for i := range activity.Users {
			activity.Users[i].DisplayName, cut = truncateRunes(activity.Users[i].DisplayName, maxText)
			truncated = truncated || cut
		}
		native.Activity = &activity
	}
	claims := documentNative(native)
	return documentItem{ID: fmt.Sprintf("document-observation:%d", row.ID), SourceID: row.SourceID,
		CaptureID: row.CaptureID, Application: row.Application, Surface: row.Document.Surface,
		Title: title, URL: row.Document.URL, WebURL: row.Document.WebURL, Native: &claims,
		ModifiedAt: searchTime(row.Document.Modified.Value), CapturedAt: row.CapturedAt,
		SourceObservedAt: row.SourceObservedAt, Retained: row.Retained,
		AssociationState: "unresolved", TextState: "not_captured", TextTruncated: truncated}
}

type documentDetailItem struct {
	documentItem
	References          []store.DocumentReferenceObservation `json:"references"`
	ReferencesTotal     int                                  `json:"references_total"`
	ReferencesTruncated bool                                 `json:"references_truncated"`
	// UsageRelations is present only on a OneDrive usage row.
	UsageRelations *usageRelationsItem `json:"usage_relations,omitempty"`
}

func documentDetailKeys() []string {
	return append(typeKeys[documentItem](), typeKeys[documentDetailItem]()...)
}

var runDocumentsSync = syncer.SyncDocuments

func (rt *runtime) documentsCoverage(sources []store.KnowledgeSource) *noteCoverage {
	out := notesCoverage(sources)
	out.Scope = "office_document_metadata"
	seen := map[string]bool{}
	for i, source := range sources {
		seen[source.Reason] = true
		for code, count := range source.Losses {
			seen[code] = seen[code] || count > 0
		}
		if documentFailureFix[source.Reason] == "" && source.Status != "failed" && source.Status != "cancelled" {
			continue
		}
		fallback, flag := "office_json_capture_failed", ""
		switch source.Kind {
		case store.RegistryDocumentKind:
			fallback, flag = "office_registry_capture_failed", "--registry-file"
		case store.UsageDocumentKind:
			fallback, flag = "onedrive_usage_capture_failed", "--onedrive-usage-file"
		}
		fix := firstOf(documentFailureFix[source.Reason], documentFailureFix[fallback])
		if flag != "" {
			// A locator with no admitted capture has no source ID to name, so its retry route is the file.
			retry := rt.recoveryCommand(false, "", "docs", "sync") + " " + flag + " " + fileRetryArgument(source.Platform, source.Locator)
			if source.ID > 0 {
				retry = rt.recoveryCommand(false, "", "docs", "sync", "--source-id", strconv.FormatInt(source.ID, 10))
			}
			fix += ". Retry this source with " + retry
		}
		out.Sources[i].Fix = fix
		out.Sources[i].Effect = "nothing has been captured from this source yet, so it adds no listed documents"
		if source.AdmittedID > 0 {
			out.Sources[i].Effect = "the last good capture stays current, so the documents it listed are still listed"
		}
	}
	for _, notice := range documentNotices {
		if seen[notice.code] {
			out.Notices = append(out.Notices, notice.text)
		}
	}
	return out
}

// windowsQuotable is what a Windows path may hold and still be literal inside double quotes in
// both cmd.exe and PowerShell: no $, backtick, %, ', &, ^ or !.
const windowsQuotable = argSafeWindows + ` ,()~#[]{}`

const argSafeWindows = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:@+=-" + `\`

// fileRetryArgument quotes the single-file selector of a source with no admitted ID.
// A fix is text an agent may paste into a shell, so the file name is
// quoted for the platform the source was registered on. A Windows name that no single quoting
// keeps literal in both cmd.exe and PowerShell is not interpolated: the fix names the locator
// field instead.
func fileRetryArgument(platform, locator string) string {
	if platform != "windows" {
		return quoteFor("")(locator)
	}
	if locator != "" && strings.Trim(locator, argSafeWindows) == "" {
		return locator
	}
	if strings.Trim(locator, windowsQuotable) == "" {
		return `"` + locator + `"`
	}
	return "FILE, where FILE is this source's locator passed as one literal argument (its name holds characters a shell would expand, so it is not repeated here)"
}

// registryFileArg is the --registry-file argument for a quotable path.
func registryFileArg(platform, locator string) string {
	return fileRetryArgument(platform, locator)
}

// documentFailureReasons are the reasons a document source capture fails or is cancelled, as
// docs/commands.md documents them; each has a remedy in documentFailureFix.
var documentFailureReasons = []string{"office_json_unrecognized", "office_json_too_large", "office_json_unreadable",
	"office_surface_unknown", "office_json_input_unqualified", "office_json_capture_failed", "office_json_cleanup_failed",
	"office_json_policy_restore_failed", "office_json_cancelled", "stale_attempt",
	"office_registry_input_unqualified", "office_registry_source_busy", "office_registry_index_unreadable",
	"office_registry_index_too_large", "office_registry_index_unrecognized", "office_registry_capture_failed",
	"office_registry_cleanup_failed", "office_registry_policy_restore_failed", "office_registry_cancelled",
	"onedrive_usage_input_unqualified", "onedrive_usage_source_busy", "onedrive_usage_index_unreadable",
	"onedrive_usage_index_too_large", "onedrive_usage_index_unrecognized", "onedrive_usage_capture_failed",
	"onedrive_usage_cleanup_failed", "onedrive_usage_policy_restore_failed", "onedrive_usage_cancelled"}

const documentsIssue = "https://github.com/ourostack/m365crawl/issues"

var documentFailureFix = map[string]string{
	"office_json_unrecognized": "the saved file is not JSON, or is JSON in a shape this m365crawl version does not read. Upgrade m365crawl and run m365crawl docs sync again; if the newest version still fails, open an issue at " +
		documentsIssue + " asking for support for this shape, with the source kind and the file's top-level key names (no values)",
	"office_json_too_large":                 "the file is larger than the reader accepts (8 MiB, or too many rows, fields or nesting levels), so nothing from it was kept. If Office wrote it, open an issue at " + documentsIssue,
	"office_json_unreadable":                "the file could not be read to the end. Run m365crawl docs sync again; if it keeps failing, check that the file is complete and readable",
	"office_surface_unknown":                "the source is registered for a surface this version does not read. Upgrade m365crawl",
	"office_json_input_unqualified":         "the path is missing, a link, on a cloud or placeholder provider, remote, or not an ordinary local file, so it was refused before reading. A file Office removed needs nothing; otherwise copy it to an ordinary local folder and run m365crawl docs sync --root DIR",
	"office_json_capture_failed":            "the private copy or its checks failed, often because the file changed while it was copied. Run m365crawl docs sync again; run m365crawl doctor if it keeps failing",
	"office_json_cleanup_failed":            "the private temporary copy could not be removed. Check free space and permissions of the temporary directory, then run m365crawl docs sync again",
	"office_json_policy_restore_failed":     "a capture safety setting could not be restored. Run m365crawl docs sync again; if it repeats, open an issue at " + documentsIssue,
	"office_json_cancelled":                 "the capture was interrupted or timed out. Run m365crawl docs sync again",
	"office_registry_input_unqualified":     "the registry file is missing, a link, hard-linked, on a cloud or placeholder provider, remote, not an ordinary local file, not a SQLite database in rollback-journal format, or has a write-ahead log or shared-memory file beside it, so it was refused before reading. A database in write-ahead mode stays refused however often the sync is retried. Otherwise copy the registry file alone to an ordinary local folder. On Windows also pass the path with the exact letter case and long names the directory lists (not an 8.3 short name such as RUNNER~1), keep every folder above the file to at most 4096 entries (not Downloads or Temp), and retry a just-written copy after a few seconds, because the directory's recorded size and time can lag",
	"office_registry_source_busy":           "an Office app was writing the registry file (it held a write lock or left a rollback journal), so nothing was read. Wait a moment and retry. If it stays busy, a rollback journal was left beside the file: it stays busy until Office opens the file again and finishes its write, so start the Office app that owns it, let it settle, and retry; m365crawl never replays or removes the journal",
	"office_registry_index_unreadable":      "the registry file could not be read as a database to the end. Check that the file is a complete copy",
	"office_registry_index_too_large":       "the registry file is larger than the reader accepts (64 MiB, or too many rows), so nothing from it was kept. If Office wrote it, open an issue at " + documentsIssue,
	"office_registry_index_unrecognized":    "the file is not an Office registry database, or has a shape this m365crawl version does not read. If it is a OneDrive usage file, register it with m365crawl docs sync --onedrive-usage-file FILE instead; this source stays listed as failed, because a file's kind is known only after it has been registered and read. Otherwise upgrade m365crawl; if the newest version still fails, open an issue at " + documentsIssue + " with the table and column names (no values)",
	"office_registry_capture_failed":        "the private copy or its checks failed, often because the file changed while it was copied. Run m365crawl doctor if it keeps failing",
	"office_registry_cleanup_failed":        "the private temporary copy could not be removed. Check free space and permissions of the temporary directory",
	"office_registry_policy_restore_failed": "a capture safety setting could not be restored. If it repeats, open an issue at " + documentsIssue,
	"office_registry_cancelled":             "the capture was interrupted or timed out",
	"onedrive_usage_input_unqualified":      "the OneDrive usage file is missing, a link, hard-linked, on a cloud or placeholder provider, remote, not an ordinary local file, not a SQLite database in rollback-journal format, or has a write-ahead log or shared-memory file beside it, so it was refused before reading. m365crawl never consolidates a write-ahead log. A database in write-ahead mode stays refused however often the sync is retried. One in rollback-journal mode that only has a -wal or -shm file beside it can pass later: run the sync again, or copy the usage file alone, once it has neither file beside it, to an ordinary local folder. On Windows also pass the path with the exact letter case and long names the directory lists, and keep every folder above the file to at most 4096 entries",
	"onedrive_usage_source_busy":            "OneDrive was writing the usage file (it held a write lock or left a rollback journal), so nothing was read. Wait a moment and retry; m365crawl never replays or removes the journal",
	"onedrive_usage_index_unreadable":       "the usage file could not be read as a database to the end. Check that the file is a complete copy",
	"onedrive_usage_index_too_large":        "the usage file is larger than the reader accepts (512 MiB, 65536 recent rows, 65536 documents, 4096 collaborators, 262144 sharing instances or participants, a 1 MiB field or 64 MiB of text), so nothing from it was kept. If OneDrive wrote it, open an issue at " + documentsIssue,
	"onedrive_usage_index_unrecognized":     "the file is not a OneDrive usage database, or has a shape this m365crawl version does not read. If it is an Office registry file, register it with m365crawl docs sync --registry-file FILE instead; this source stays listed as failed, because a file's kind is known only after it has been registered and read. Otherwise upgrade m365crawl; if the newest version still fails, open an issue at " + documentsIssue + " with the table and column names (no values)",
	"onedrive_usage_capture_failed":         "the private copy or its checks failed, often because the file changed while it was copied. Run m365crawl doctor if it keeps failing",
	"onedrive_usage_cleanup_failed":         "the private temporary copy could not be removed. Check free space and permissions of the temporary directory",
	"onedrive_usage_policy_restore_failed":  "a capture safety setting could not be restored. If it repeats, open an issue at " + documentsIssue,
	"onedrive_usage_cancelled":              "the capture was interrupted or timed out (30 seconds for one file, including the wait for other captures)",
	"stale_attempt":                         "another capture of the same source finished first, so this one was not admitted. Run m365crawl docs sources to see the current state",
}

const documentsListedNoneNotice = officedocuments.NoDocumentsListed + ": each source with this reason last saved a successful Office response that listed no documents, so its capture is complete with zero rows. " +
	"This describes the saved response, not whether the account has documents; earlier observations from the source stay in the archive with retained=true."

const documentsFileListedNoneNotice = officedocuments.FileListedNoDocuments + ": each source with this reason last saved an Office dialog list or a supplied file (docs sync --root) that listed no documents, so its capture is complete with zero rows. " +
	"This describes the saved Office JSON file, not whether the account has documents; earlier observations from the source stay in the archive with retained=true."

const documentsRegistryListedNoneNotice = syncer.RegistryListedNoDocuments + ": each source with this reason is an Office registry file whose recently-opened list held no documents, so its capture is complete with zero rows. " +
	"This describes the registry file, not whether the account has documents; earlier observations from the source stay in the archive with retained=true."

const documentsRegistryUnmappedNotice = "office_registry_document_unmapped: a registry source with this loss holds recently-opened entries this m365crawl version could not map to a document (for example no title or no URL), so its coverage is partial. The mapped entries are listed; the unmapped ones are counted, not kept."

const documentsUsageListedNothingNotice = syncer.UsageListedNothing + ": each source with this reason is a OneDrive usage file that held no recent row, no document and no collaborator, so its capture is complete with zero rows. " +
	"This describes the usage file, not whether the account has documents; earlier observations from the source stay in the archive with retained=true."

// documentNotices explain, in a fixed order, a complete capture that listed nothing and a
// registry or usage capture that could not map every row.
var documentNotices = []struct{ code, text string }{
	{officedocuments.NoDocumentsListed, documentsListedNoneNotice},
	{officedocuments.FileListedNoDocuments, documentsFileListedNoneNotice},
	{syncer.RegistryListedNoDocuments, documentsRegistryListedNoneNotice},
	{"office_registry_document_unmapped", documentsRegistryUnmappedNotice},
	{syncer.UsageListedNothing, documentsUsageListedNothingNotice},
	{"onedrive_usage_recent_unmapped", "onedrive_usage_recent_unmapped: a usage source with this loss holds recent rows this m365crawl version could not map (for example no ID, or a value of the wrong type), so its coverage is partial. The mapped rows are listed; the unmapped ones are counted in the source's losses, not kept."},
	{"onedrive_usage_document_unmapped", "onedrive_usage_document_unmapped: a usage source with this loss holds formatted document rows this m365crawl version could not map (for example no Format, or a value that is not the expected JSON), so its coverage is partial. The mapped rows are listed; the unmapped ones are counted in the source's losses, not kept."},
	{"onedrive_usage_document_orphan", "onedrive_usage_document_orphan: a usage source with this loss holds formatted document rows whose native ID has no recent row in the same file, so its coverage is partial. They are counted in the source's losses, not kept."},
	{"onedrive_usage_collaborator_unmapped", "onedrive_usage_collaborator_unmapped: a usage source with this loss holds collaborator rows this m365crawl version could not map, so its coverage is partial. The mapped rows are listed; the unmapped ones are counted in the source's losses, not kept."},
}

// documentDiscoveryItem is one default discovery outcome in docs sync: what was not found or was
// refused, and how to supply that family's input by hand. Locator, when present, is the one
// default location the outcome is about; without it the outcome is about the whole family. It
// names no source.
type documentDiscoveryItem struct {
	Kind    string `json:"kind"`
	Code    string `json:"code"`
	Locator string `json:"locator,omitempty"`
	Meaning string `json:"meaning"`
}

var documentDiscoveryMeaning = map[string]string{
	"office_registry_unavailable":          "no default Office registry was found: this machine has no Mac Office registry folder, or it holds no registry file",
	"office_registry_default_unsupported":  "this platform has no default Office registry location",
	"office_registry_input_unqualified":    "the default Office registry folder is not an ordinary local folder, or holds more files than the reader accepts, so it was refused before reading",
	"office_json_input_unqualified":        "the default Office JSON caches are missing or are not ordinary local files, so they were refused before reading",
	"onedrive_usage_unavailable":           "no default OneDrive usage file was found: this machine has no Mac OneDrive ListSync settings folder, or it holds no account folder with a usage file",
	"onedrive_usage_default_unsupported":   "this platform has no default OneDrive usage location",
	"onedrive_usage_input_unqualified":     "the default OneDrive settings folder, an account folder in it or a usage file is not an ordinary local folder or file, or the folder holds more entries or accounts than the reader accepts (4096 entries, 16 accounts), so the whole family was refused before reading",
	"onedrive_usage_discovery_unsupported": "this account folder's name is not one this version reads by default (only Business_ followed by a GUID), so nothing in it was read",
}

// documentLocatedMeaning is the meaning of a code when the outcome is about one default location.
var documentLocatedMeaning = map[string]string{
	"onedrive_usage_unavailable": "this account folder holds no OneDrive usage file",
}

func documentDiscoveryItems(outcomes []syncer.DocumentDiscovery) []documentDiscoveryItem {
	out := []documentDiscoveryItem{}
	for _, outcome := range outcomes {
		route, what := "m365crawl docs sync --root DIR", "the default Office JSON caches could not be used"
		switch outcome.Kind {
		case store.RegistryDocumentKind:
			route, what = "m365crawl docs sync --registry-file FILE", "the default Office registry could not be used"
		case store.UsageDocumentKind:
			route, what = "m365crawl docs sync --onedrive-usage-file FILE", "the default OneDrive usage files could not be used"
		}
		scope, meaning := "this family", documentDiscoveryMeaning[outcome.Code]
		if outcome.Locator != "" {
			scope, meaning = "this location", firstOf(documentLocatedMeaning[outcome.Code], meaning)
		}
		out = append(out, documentDiscoveryItem{Kind: outcome.Kind, Code: outcome.Code, Locator: outcome.Locator,
			Meaning: firstOf(meaning, what) + ". Nothing was registered or read for " + scope + "; registered sources and the other families were still tried. Supply an ordinary local copy with " + route})
	}
	return out
}

// documentsNothingUsable is the coded failure of a default sync in which no discovery family was
// usable and no registered source was attempted: nothing was read, so it is not a success. The
// code is the Office JSON family's, as it was before the other families existed.
func documentsNothingUsable(report syncer.DocumentReport) error {
	families := map[string]string{}
	for _, outcome := range report.Discovery {
		families[outcome.Kind] = firstOf(families[outcome.Kind], outcome.Code)
	}
	if len(report.Sources) > 0 || families[syncer.DocumentFamilyJSON] == "" || families[store.RegistryDocumentKind] == "" ||
		families[store.UsageDocumentKind] == "" {
		return nil
	}
	return &errs.Coded{Code: families[syncer.DocumentFamilyJSON], Exit: errs.ExitEnvironment,
		Message: "no Office document input was usable and no document source is registered (office_json: " + families[syncer.DocumentFamilyJSON] +
			"; office_registry: " + families[store.RegistryDocumentKind] + "; onedrive_usage: " + families[store.UsageDocumentKind] + "); nothing was read",
		Fix: "Supply ordinary local copies: `m365crawl docs sync --root DIR` for Office JSON files, `m365crawl docs sync --registry-file FILE` for an Office registry file, or `m365crawl docs sync --onedrive-usage-file FILE` for a OneDrive usage file."}
}

func documentError(err error) error {
	var source *officecapture.ReadError
	if errors.As(err, &source) {
		return &errs.Coded{Code: source.Code, Exit: errs.ExitEnvironment,
			Message: "Office document metadata input was refused; no document was opened or fetched",
			Fix:     "Use supported ordinary local Office JSON copies on a qualified local disk with `docs sync --root DIR`, one ordinary local Office registry copy with `docs sync --registry-file FILE`, or one ordinary local OneDrive usage copy with `docs sync --onedrive-usage-file FILE`; provider/placeholder, linked and unrecognized inputs are not accepted."}
	}
	if errors.Is(err, syncer.ErrRegistryFileUnusable) {
		return errs.Usage("--registry-file must name an existing regular file that is not the archive database or one of its own files; nothing was registered")
	}
	if errors.Is(err, syncer.ErrUsageFileUnusable) {
		return errs.Usage(usageFileUnusable)
	}
	if errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		return &errs.Coded{Code: "document_source_unknown", Exit: errs.ExitUsage,
			Message: "document source not found", Fix: "Run `m365crawl docs sources` for registered source IDs."}
	}
	if errors.Is(err, store.ErrDocumentObservationNotFound) || errors.Is(err, store.ErrNoArchive) {
		return &errs.Coded{Code: "document_observation_unknown", Exit: errs.ExitUsage,
			Message: "document observation not found", Fix: "Run `m365crawl docs list` for retained document-observation:IDs."}
	}
	return asCoded(err)
}

func (rt *runtime) documentFlags() error {
	if rt.g.Account != "" || rt.g.OutlookAccount != "" || rt.g.OutlookProfile != "" {
		return errs.Usage("document account/subject ownership is unresolved; drop account/link/profile filters")
	}
	return nil
}

func (rt *runtime) refreshDocuments(sourceID int64) (*noteCoverage, error) {
	var report syncer.DocumentReport
	refreshErr := rt.besideOtherRun(func() (err error) {
		report, err = runDocumentsSync(rt.ctx, syncer.DocumentOptions{DBPath: rt.dbPath, SourceID: sourceID, MaxAge: rt.maxAge})
		return err
	})
	if refreshErr == nil {
		return rt.documentsCoverage(report.Sources), nil
	}
	if rt.ctx.Err() != nil || errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return nil, refreshErr
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return nil, errors.Join(refreshErr, err)
	}
	sources, readErr := st.DocumentSources(rt.ctx)
	closeErr := st.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, errors.Join(refreshErr, err)
	}
	selected := []store.KnowledgeSource{}
	for _, source := range sources {
		if sourceID == 0 || source.ID == sourceID {
			selected = append(selected, source)
		}
	}
	if sourceID > 0 && len(selected) == 0 {
		return nil, refreshErr
	}
	coverage := rt.documentsCoverage(selected)
	coded := errs.DBError(refreshErr)
	errors.As(documentError(refreshErr), &coded)
	coverage.RefreshError = &syncError{Code: coded.Code, Message: coded.Message}
	return coverage, nil
}

func (rt *runtime) documentsRead(label string, sourceID int64, fn func(*store.Store) (*listResult, error)) error {
	if err := rt.documentFlags(); err != nil {
		return err
	}
	if sourceID < 0 {
		return errs.Usage("--source-id must be a positive document source ID")
	}
	coverage, err := rt.refreshDocuments(sourceID)
	if err != nil {
		return documentError(err)
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		st = nil
	} else if err != nil {
		return documentError(err)
	}
	result, readErr := fn(st)
	var closeErr error
	if st != nil {
		closeErr = st.Close()
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return documentError(err)
	}
	if result.SourceCoverage == nil {
		result.SourceCoverage = rt.documentsCoverage(nil)
	}
	result.SourceCoverage.RefreshError = coverage.RefreshError
	result.sourceOnly = true
	if result.Count == 0 {
		if len(result.SourceCoverage.Sources) == 0 {
			result.Note = "no document metadata captured yet; run m365crawl docs sync, docs sync --root DIR, docs sync --registry-file FILE or docs sync --onedrive-usage-file FILE"
		} else {
			result.Note = "no document metadata matched; source_coverage describes retained or unavailable inputs"
		}
	}
	rt.noteRegisteredSyncOff(result, "document")
	return rt.write(label, result)
}

type documentsSyncCmd struct {
	Root         string `help:"Ordinary local directory with recent.json, shared.json, recommended.json or dialog.json; never a provider/placeholder path. Office JSON only." placeholder:"DIR"`
	RegistryFile string `name:"registry-file" help:"One ordinary local Office registry file (a copy of MicrosoftRegistrationDB_*.reg), as a clean absolute path; never a provider/placeholder path. Registry only." placeholder:"FILE"`
	UsageFile    string `name:"onedrive-usage-file" help:"One ordinary local OneDrive usage file (a copy of Microsoft.FileUsageSync.db, without a -wal or -shm file beside it), as a clean absolute path; never a provider/placeholder path. OneDrive usage only." placeholder:"FILE"`
	SourceID     int64  `name:"source-id" help:"Refresh only this positive registered document source ID. --root, --registry-file, --onedrive-usage-file and --source-id are mutually exclusive."`
}

const usageFileUnusable = "--onedrive-usage-file must name an existing regular file that is not the archive database or one of its own files; nothing was registered"

func (documentsSyncCmd) Help() string {
	return "With no selection, docs sync refreshes every registered document source and tries each default family on its own: the named Mac Office JSON caches, the Mac Office registry (" +
		"~/Library/Group Containers/UBF8T346G9.Office/MicrosoftRegistrationDB/MicrosoftRegistrationDB_*.reg, at most 16 files) and the Mac OneDrive usage files (" +
		"~/Library/Application Support/OneDrive/settings/ListSync/Business_<GUID>/Microsoft.FileUsageSync.db, at most 16 account folders). A family that is absent or refused is reported under discovery with a fixed code and never stops another family or a registered source; a discovery entry with a locator is about that one default location (an account folder whose name is not read, or one with no usage file). There is no default discovery on Windows or Linux.\n" +
		"--root DIR reads only Office JSON files in DIR; --registry-file FILE reads only that one registry file; --onedrive-usage-file FILE reads only that one OneDrive usage file; --source-id N refreshes only that registered source. Each failed source keeps its own status, reason and fix in source_coverage; a source that has never been captured has source_id null and its fix names the --registry-file or --onedrive-usage-file retry.\n" +
		"--registry-file FILE and --onedrive-usage-file FILE must each be an existing regular file that is not the archive database: otherwise the command is a usage error and registers nothing. A file that was opened and then refused is registered as a failed source, so its reason and fix stay visible in docs sources and every docs read. There is no command to remove a source: it stays listed as failed until a later sync of it succeeds.\n" +
		"A OneDrive usage file is read the same way as a registry file: from a private copy, taken while OneDrive is held out of writing it, of at most 512 MiB, within 30 seconds. A file with a write-ahead log or shared-memory file beside it is refused with onedrive_usage_input_unqualified and never consolidated. A database in write-ahead mode stays refused however often the sync is retried; one in rollback-journal mode can pass once neither file is beside it, or from an ordinary local copy that has neither. Only the usage file is read: OneDrive's library and sync databases are not.\n" +
		"A registry file is read from a private copy taken while Office is held out of writing it; a file Office is writing is refused (run the sync again later), and so is one with a write-ahead log beside it, which stays refused while the database is in write-ahead mode. A rollback journal left beside the file (Office is mid-write, or quit before finishing) keeps the source at office_registry_source_busy until Office opens the file again and finishes; m365crawl never replays or removes the journal. " +
		"On Windows the path must use the folder and file names exactly as the directory lists them (same letter case, long names not 8.3 short names), and every folder above the file must hold at most 4096 entries, so copy the file to a small folder rather than Downloads or Temp.\n" + documentRegistryHelp
}

func (c *documentsSyncCmd) Run(rt *runtime) error {
	if err := rt.documentFlags(); err != nil {
		return err
	}
	selections := 0
	for _, selected := range []bool{c.Root != "", c.RegistryFile != "", c.UsageFile != "", c.SourceID != 0} {
		if selected {
			selections++
		}
	}
	if c.SourceID < 0 || selections > 1 {
		return errs.Usage("choose one of --root DIR, --registry-file FILE, --onedrive-usage-file FILE or one positive --source-id, not several")
	}
	if c.UsageFile != "" && (!filepath.IsAbs(c.UsageFile) || filepath.Clean(c.UsageFile) != c.UsageFile) {
		return errs.Usage("--onedrive-usage-file must be a clean absolute path to one OneDrive usage file")
	}
	if c.UsageFile != "" && syncer.CheckUsageFile(rt.dbPath, c.UsageFile) != nil {
		return errs.Usage(usageFileUnusable)
	}
	if c.RegistryFile != "" && (!filepath.IsAbs(c.RegistryFile) || filepath.Clean(c.RegistryFile) != c.RegistryFile) {
		return errs.Usage("--registry-file must be a clean absolute path to one Office registry file")
	}
	if c.RegistryFile != "" && syncer.CheckRegistryFile(rt.dbPath, c.RegistryFile) != nil {
		return errs.Usage("--registry-file must name an existing regular file that is not the archive database or one of its own files; nothing was registered")
	}
	report, err := runDocumentsSync(rt.ctx, syncer.DocumentOptions{DBPath: rt.dbPath, Root: c.Root, RegistryFile: c.RegistryFile,
		UsageFile: c.UsageFile, SourceID: c.SourceID, Explicit: true, Discover: c.SourceID == 0})
	if err != nil {
		return documentError(err)
	}
	if err := documentsNothingUsable(report); err != nil {
		return err
	}
	return rt.write("docs sync", notesSyncResult{Captured: report.Captured, Unchanged: report.Unchanged,
		Skipped: report.Skipped, Failed: report.Failed, SourceCoverage: rt.documentsCoverage(report.Sources),
		Discovery: documentDiscoveryItems(report.Discovery)})
}

type documentsListCmd struct {
	SourceID    int64  `name:"source-id" help:"Only this registered document source ID; docs sources lists IDs."`
	Query       string `help:"Words in document title, native URLs/IDs or source sharing/person metadata."`
	Since       string `help:"Native modification time at or after this date/time; unknown dates excluded. Never matches a registry row or a collaborator row: neither states a modification time. Recent rows match only when their file-modification clock has a parsed explicit offset; missing, unparsed or zone-less clocks are excluded. The result counts the rows left out."`
	Until       string `help:"Native modification time before this time; a date alone includes that whole local day, a time is exclusive. Never matches a registry row or a collaborator row: neither states a modification time. Recent rows match only when their file-modification clock has a parsed explicit offset; missing, unparsed or zone-less clocks are excluded. The result counts the rows left out."`
	Limit       int    `default:"50" help:"Maximum observations; 0 returns all. Check truncated."`
	Application string `help:"Only this source label: Word, Excel or PowerPoint (any case) for Office JSON rows, supplied for files given with docs sync --root, registry for Office registry rows, or onedrive for OneDrive usage rows. Narrows rows, not source_coverage." placeholder:"APP"`
	Surface     string `help:"Only this Office JSON surface: recent, shared, recommended or dialog; docs --help defines each. Registry and OneDrive usage rows have no surface and never match. Narrows rows, not source_coverage." placeholder:"SURFACE"`
	OrderBy     string `name:"order-by" enum:"modified_at,opened_at" default:"modified_at" help:"modified_at (the default order) or opened_at: rows that state an opened time first, newest first, then the rest in the default order. Only registry rows state one." placeholder:"KEY"`
	label       string // the result label: docs search runs this command under its own name
	search      bool   // docs search: it accepts no --order-by, so no hint names it
}

// documentApplication is --application's value as stored, or a usage error naming the accepted values.
func documentApplication(raw string) (string, error) {
	for _, app := range []string{"Word", "Excel", "PowerPoint", "supplied", store.RegistryDocumentSource, store.UsageDocumentSource} {
		if strings.EqualFold(raw, app) {
			return app, nil
		}
	}
	if raw == "" {
		return "", nil
	}
	return "", errs.Usage("--application must be Word, Excel, PowerPoint, supplied (a file given with docs sync --root), registry (an Office registry row) or onedrive (a OneDrive usage row), not " + strconv.Quote(raw))
}

// documentSurface is --surface's value, or a usage error naming the accepted values.
func documentSurface(raw string) (officedocuments.Surface, error) {
	surface := officedocuments.Surface(strings.ToLower(raw))
	if raw == "" || documentSurfaces[surface] != "" {
		return surface, nil
	}
	return "", errs.Usage("--surface must be one of recent, shared, recommended, dialog, not " + strconv.Quote(raw))
}

const documentEvidenceHelp = "Each row is an immutable native metadata representation, not a resolved file/owner or a newly occurring sharing event. Identical sharing claims and repeat capture do not create new events; missing native sharing/activity dates stay unknown.\n" +
	"capture_id and captured_at identify the representation's original private acquisition, not its most recent check. source_observed_at is the acquired input file's modification time, not native document/sharing time or admission time. Later checks do not overwrite these original clocks. retained means this representation was absent from the last admitted capture; absence does not mean deletion or revoked sharing.\n" +
	documentOrderHelp + " docs --help defines surfaces, unresolved states, Sharing.Type and each clock.\n" +
	"A registry row (variant office_registry) carries registry_native and opened_at instead of native: opened_at is the registry's opened time or null, modified_at is always null, and no surface applies.\n" +
	"A OneDrive usage row (variant onedrive_usage) carries usage_native and full_inspection instead of native: usage_native.Type is recent, document or collaborator, title and url are a document row's own and empty otherwise, modified_at is the file modification time or null, and no surface applies. Sharing history and participants are previewed at 100 with full counts; docs --help defines every usage field.\n" +
	"Native timestamps keep exact source text in Raw; Value is parsed UTC or null when missing/unrecognized. They are never replaced with file/capture time. modified_at is the native document modification time or null; bounded date windows exclude unknown values. text_state=not_captured means original document contents were not acquired."

func (documentsListCmd) Help() string {
	return documentEvidenceHelp + "\n--order-by opened_at lists the most recently opened first: `m365crawl docs list --application registry --order-by opened_at --limit 5` is the five registry documents opened last. Rows without an opened time follow in the default order. OneDrive usage rows state no opened time: a recent row's LastAccessed is kept as the file wrote it, without a zone in the files examined so far, and is not used for ordering."
}

// documentOrderHint is the text line of a default-order list in which no returned row has a
// modification time: the order is then by title, and the flag that orders by opened time exists.
const documentOrderHint = "no returned row states a modification time, so they are in title order; --order-by opened_at puts rows that state an opened time (registry rows) first, newest first"

// undatedKinds are the kinds of row a date window can leave out for want of a modification time,
// in the order the notice names them, each with its singular, its plural and the reason.
var undatedKinds = []struct{ key, one, many, why string }{
	{store.DocumentVariantJSON, "Office JSON row", "Office JSON rows", "Office gave no modification time that parses"},
	{store.RegistryDocumentKind, "Office registry row", "Office registry rows", "the registry states none"},
	{store.UsageDocumentKind + "_" + store.UsageTypeRecent, "OneDrive usage recent row", "OneDrive usage recent rows", "its clock has no zone or is missing, so it is not placed in time"},
	{store.UsageDocumentKind + "_" + store.UsageTypeDocument, "OneDrive usage document row", "OneDrive usage document rows", "its clock is missing or has no zone"},
	{store.UsageDocumentKind + "_" + store.UsageTypeCollaborator, "OneDrive usage collaborator row", "OneDrive usage collaborator rows", "a profile has none"},
}

// undatedNotice says how many rows a date window left out because they have no modification
// time, by kind and with the reason, or is empty when it left none out.
func undatedNotice(undated map[string]int) string {
	total, parts := 0, []string{}
	for _, kind := range undatedKinds {
		if n := undated[kind.key]; n > 0 {
			total += n
			parts = append(parts, plural(n, kind.one, kind.many)+" ("+kind.why+")")
		}
	}
	if total == 0 {
		return ""
	}
	list := parts[0]
	if len(parts) > 1 {
		list = strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
	if total == 1 {
		return "--since/--until left out 1 row that otherwise matched because it has no modification time: " + list + ". Drop the date filter to list it"
	}
	return fmt.Sprintf("--since/--until left out %d rows that otherwise matched because they have no modification time: %s. Drop the date filter to list them", total, list)
}

func (c *documentsListCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "docs list", ""); err != nil {
		return err
	}
	if c.Limit < 0 {
		return errs.Usage("--limit must be nonnegative; 0 returns all document observations")
	}
	since, until, err := rt.window("--since", c.Since, "--until", c.Until)
	if err != nil {
		return err
	}
	application, err := documentApplication(c.Application)
	if err != nil {
		return err
	}
	surface, err := documentSurface(c.Surface)
	if err != nil {
		return err
	}
	return rt.documentsRead(firstOf(c.label, "docs list"), c.SourceID, func(st *store.Store) (*listResult, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		snapshot, err := st.DocumentSnapshot(rt.ctx, store.DocumentFilter{Query: c.Query,
			SourceID: c.SourceID, Application: application, Surface: surface, ModifiedSince: since, ModifiedUntil: until, Limit: c.Limit,
			OrderBy: c.OrderBy})
		if err != nil {
			return nil, err
		}
		items := make([]documentItem, len(snapshot.Rows))
		undated := len(items) > 0 && !c.search && c.OrderBy != store.DocumentOrderOpened
		for i, row := range snapshot.Rows {
			items[i] = documentItemOf(row, rt.g.MaxText, rt.archiveRef())
			undated = undated && items[i].ModifiedAt == nil
		}
		result := newList(shape(rt, items), snapshot.Truncated)
		result.SourceCoverage = rt.documentsCoverage(snapshot.Sources)
		if notice := undatedNotice(snapshot.Undated); notice != "" {
			result.addNotice(notice)
		}
		if undated {
			result.rowsHint = documentOrderHint
		}
		return result, nil
	})
}

type documentsSearchCmd struct {
	Query       string `arg:"" optional:"" help:"Words in archived document metadata; required unless a date, --application or --surface filter is supplied."`
	SourceID    int64  `name:"source-id" help:"Only this registered document source ID."`
	Since       string `help:"Native modification time at or after this date/time. Never matches a registry row or a collaborator row: neither states a modification time. Recent rows match only when their file-modification clock has a parsed explicit offset; missing, unparsed or zone-less clocks are excluded. The result counts the rows left out."`
	Until       string `help:"Native modification time before this time; a date alone includes that whole local day, a time is exclusive; unknown dates excluded. Never matches a registry row or a collaborator row: neither states a modification time. Recent rows match only when their file-modification clock has a parsed explicit offset; missing, unparsed or zone-less clocks are excluded. The result counts the rows left out."`
	Limit       int    `default:"50" help:"Maximum observations; 0 returns all. Check truncated."`
	Application string `help:"Only this source label: Word, Excel or PowerPoint (any case) for Office JSON rows, supplied for files given with docs sync --root, registry for Office registry rows, or onedrive for OneDrive usage rows. Narrows rows, not source_coverage." placeholder:"APP"`
	Surface     string `help:"Only this Office JSON surface: recent, shared, recommended or dialog; docs --help defines each. Registry and OneDrive usage rows have no surface and never match. Narrows rows, not source_coverage." placeholder:"SURFACE"`
}

func (documentsSearchCmd) Help() string { return documentEvidenceHelp }

func (c *documentsSearchCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "docs search", ""); err != nil {
		return err
	}
	if strings.TrimSpace(c.Query) == "" && c.Since == "" && c.Until == "" && c.Application == "" && c.Surface == "" {
		return errs.Usage("docs search needs words, a native date filter, --application or --surface; use docs list for an unfiltered list")
	}
	return (&documentsListCmd{Query: c.Query, SourceID: c.SourceID, Since: c.Since, Until: c.Until, Limit: c.Limit,
		Application: c.Application, Surface: c.Surface, label: "docs search", search: true}).Run(rt)
}

type documentsShowCmd struct {
	ID string `arg:"" help:"document-observation:ID returned by docs list/search or shared search; not a URL/native ID."`
}

func (documentsShowCmd) Help() string {
	return documentEvidenceHelp + "\nURL references are bounded exact occurrences, not resolved identity or proof of local availability. source_coverage describes this observation's selected source; use docs sources for referenced peers' coverage.\n" +
		"For a OneDrive usage row, usage_relations lists the observations of the same capture (capture_id) with the same native ID: for a recent row its document rows, for a document row its recent row, none for a collaborator. It previews the first 100 of total; follow an item's id with docs show. A relation says the usage file used one native ID for both rows, not that they are one resolved file."
}

func (c *documentsShowCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "docs show", ""); err != nil {
		return err
	}
	if err := rt.documentFlags(); err != nil {
		return err
	}
	raw, prefixed := strings.CutPrefix(c.ID, "document-observation:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !prefixed || err != nil || id <= 0 || raw != strconv.FormatInt(id, 10) {
		return errs.Usage("document ID must be document-observation: followed by a positive decimal ID; use docs list")
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return documentError(err)
	}
	detail, readErr := st.DocumentObservation(rt.ctx, id)
	closeErr := st.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return documentError(err)
	}
	return rt.documentsRead("docs show", detail.Observation.SourceID, func(st *store.Store) (*listResult, error) {
		if st == nil {
			return nil, store.ErrDocumentObservationNotFound
		}
		current, err := st.DocumentObservation(rt.ctx, id)
		if err != nil {
			return nil, err
		}
		item := documentDetailItem{documentItem: documentItemOf(current.Observation, rt.g.MaxText, rt.archiveRef()),
			References: current.References, ReferencesTotal: current.ReferencesTotal, ReferencesTruncated: current.ReferencesTruncated,
			UsageRelations: usageRelationsOf(current)}
		result := newList(shape(rt, []documentDetailItem{item}), false)
		result.SourceCoverage = rt.documentsCoverage([]store.KnowledgeSource{current.Source})
		return result, nil
	})
}

type documentsSourcesCmd struct {
	SourceID int64 `name:"source-id" help:"Only this positive registered document source ID; this command never captures."`
}

func (c *documentsSourcesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "docs sources", ""); err != nil {
		return err
	}
	if err := rt.documentFlags(); err != nil {
		return err
	}
	if c.SourceID < 0 {
		return errs.Usage("--source-id must be a positive document source ID")
	}
	report, err := runDocumentsSync(rt.ctx, syncer.DocumentOptions{DBPath: rt.dbPath, SourceID: c.SourceID, MaxAge: 0})
	if err != nil {
		return documentError(err)
	}
	coverage := rt.documentsCoverage(report.Sources)
	result := newList(shape(rt, coverage.Sources), false)
	result.SourceCoverage, result.sourceOnly = coverage, true
	if result.Count == 0 {
		result.Note = "no document sources registered; run m365crawl docs sync, docs sync --root DIR, docs sync --registry-file FILE or docs sync --onedrive-usage-file FILE"
	}
	return rt.write("docs sources", result)
}

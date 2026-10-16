package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

type calendarAttendeesGroup struct {
	Sync    attendeeSyncCmd    `cmd:"" help:"Explicitly capture native selected-detail evidence: Mac default HxStore discovery or a supported copied profile root; no OWA acquisition."`
	Sources attendeeSourcesCmd `cmd:"" help:"Read registered native attendee sources and their independent archive-only coverage."`
	List    attendeeListCmd    `cmd:"" help:"Read an event's source-selected native observations, not its retained/unioned displayed roster. Always offline."`
	Show    attendeeShowCmd    `cmd:"" help:"Read one positive attendee-observation:ID, its own snapshot ambiguity and independent parent bindings. Always offline."`
}

func (calendarAttendeesGroup) Help() string {
	return "Native HxStore observations are source-selected detail evidence, not active people, resolved identities, responses or a current attendee roster. Existing calendar event attendees remain retained/unioned display detail.\n" +
		"Only sync acquires input. Default explicit discovery is Mac HxStore; --root accepts a supported copied <root>/<profile>/HxStore.hxd input. Windows/OWA native acquisition is not qualified. Shared ordinary Outlook capture refreshes registered lanes only, respecting its copy interval.\n" +
		"list, show and sources never refresh, discover, register, copy or migrate, even with --max-age 1ns. --history includes retained associated copies without making them current; --account still requires compatible captured/current owner scope. Absence is not evidence that a person or meeting does not exist.\n" +
		"native_attendee_evidence survives fields, zero hits and preview limits. Native physical rows are not multiplied by event bindings. Own-snapshot ambiguity is computed before parent/100-row projection. Full-record/capture routes retain the selected absolute archive and --max-age 0. Current source capture and own_captures recovery are distinct: own routes are deduplicated from at most100 rows plus100 bindings and survive --fields."
}

type attendeeSyncCmd struct {
	Root     string `help:"Supported copied profile root; mutually exclusive with --source-id. Without either selector, explicit Mac HxStore discovery." placeholder:"DIR"`
	SourceID *int64 `help:"Retry this positive admitted archive source ID using its exact registered file/account; no global discovery."`
}
type attendeeSourcesCmd struct {
	SourceID *int64 `help:"Only this positive admitted archive source ID; omitted means all registrations."`
}
type attendeeListCmd struct {
	Event    string `arg:"" help:"An archived event_id/event_key or unambiguous alias/prefix from calendar."`
	SourceID *int64 `help:"Project this positive admitted archive source ID; relevant integrity/coverage still survives zero hits."`
	History  bool   `help:"Include retained event-associated snapshots, without promoting their original read/link scope to current."`
	Limit    int    `default:"100" help:"Maximum physical observations; minimum1, hard preview100. Full counts and routes remain."`
}
type attendeeShowCmd struct {
	ID string `arg:"" help:"Positive canonical attendee-observation:ID printed by list; not a native key/name/URL."`
}

type attendeeLoss struct {
	Code  string `json:"code"`
	Count string `json:"count"`
}

type AttendeeEvidenceItem struct {
	ID                   string         `json:"id"`
	SourceID             string         `json:"source_id"`
	SnapshotID           string         `json:"snapshot_id"`
	CaptureID            string         `json:"capture_id"`
	RepresentationDigest string         `json:"representation_digest"`
	Qualification        string         `json:"qualification"`
	Ordinal              int            `json:"ordinal"`
	NativeKey            uint32         `json:"native_key"`
	DetailKey            uint32         `json:"detail_key"`
	VersionRaw           string         `json:"version_raw"`
	BlockOffset          string         `json:"block_offset"`
	PayloadPos           string         `json:"payload_pos"`
	Name                 *string        `json:"name"`
	Email                *string        `json:"email"`
	Resynced             bool           `json:"resynced"`
	Ambiguous            bool           `json:"ambiguous"`
	Retained             bool           `json:"retained"`
	AcquiredAt           time.Time      `json:"acquired_at"`
	SourceObservedAt     *time.Time     `json:"source_observed_at"`
	LossScope            string         `json:"loss_scope"`
	SourceLosses         []attendeeLoss `json:"source_losses"`
	Scope                string         `json:"scope"`
	ScopeState           string         `json:"scope_state"`
	TextTruncated        bool           `json:"text_truncated,omitempty"`
	FullRecordSQL        string         `json:"full_record_sql"`
	FullRecordCommand    string         `json:"full_record_command"`
}

type attendeeBindingItem struct {
	ID                   string `json:"binding_id"`
	SourceID             string `json:"source_id"`
	SnapshotID           string `json:"snapshot_id"`
	NativeEventID        string `json:"native_event_id"`
	DetailKey            uint32 `json:"detail_key"`
	LastModifiedRaw      string `json:"last_modified_raw"`
	StampRaw             string `json:"stamp_raw"`
	BlockOffset          string `json:"block_offset"`
	PayloadPos           string `json:"payload_pos"`
	DetailCopies         string `json:"detail_copies"`
	DetailResyncedCopies string `json:"detail_resynced_copies"`
	DetailPresent        bool   `json:"detail_present"`
	DetailDiffering      bool   `json:"detail_differing"`
	Retained             bool   `json:"retained"`
	MatchesRead          bool   `json:"matches_read"`
	MatchesOrigin        bool   `json:"matches_origin"`
	MatchesLink          bool   `json:"matches_link"`
	CalendarRowPresent   bool   `json:"calendar_row_present"`
}

type attendeeSourceItem struct {
	RegistrationID       string         `json:"registration_id"`
	SourceID             *string        `json:"source_id"`
	AttemptID            *string        `json:"attempt_id"`
	AdmittedID           *string        `json:"admitted_id"`
	SnapshotID           *string        `json:"snapshot_id"`
	CalendarReadID       *string        `json:"calendar_read_id"`
	LinkRevision         *string        `json:"link_revision"`
	CurrentReadID        *string        `json:"current_read_id"`
	CurrentLinkRevision  *string        `json:"current_link_revision"`
	Locator              string         `json:"locator"`
	Account              string         `json:"account"`
	Status               string         `json:"status"`
	Reason               string         `json:"reason"`
	RepresentationDigest string         `json:"representation_digest"`
	Qualification        string         `json:"qualification"`
	OriginState          string         `json:"origin_state"`
	CalendarState        string         `json:"calendar_state"`
	LinkState            string         `json:"link_state"`
	ScopeState           string         `json:"scope_state"`
	CapturedLink         peopleLinkItem `json:"captured_profile_link"`
	CurrentLink          peopleLinkItem `json:"current_profile_link"`
	AttendeesTotal       string         `json:"attendees_total"`
	ParentsTotal         string         `json:"parents_total"`
	BindingsTotal        string         `json:"bindings_total"`
	LossScope            string         `json:"loss_scope"`
	Losses               []attendeeLoss `json:"losses"`
	AttemptedAt          *time.Time     `json:"attempted_at"`
	AdmittedAt           *time.Time     `json:"admitted_at"`
	AcquiredAt           *time.Time     `json:"acquired_at"`
	SourceObservedAt     *time.Time     `json:"source_observed_at"`
	Retry                string         `json:"retry"`
	FullCaptureSQL       string         `json:"full_capture_sql,omitempty"`
	FullCaptureCommand   string         `json:"full_capture_command,omitempty"`
}

type attendeeCaptureItem struct {
	SnapshotID         string `json:"snapshot_id"`
	FullCaptureSQL     string `json:"full_capture_sql"`
	FullCaptureCommand string `json:"full_capture_command"`
}

type AttendeeEvidenceCoverage struct {
	Scope             string                `json:"scope"`
	EventID           string                `json:"event_id,omitempty"`
	Sources           []attendeeSourceItem  `json:"sources"`
	Bindings          []attendeeBindingItem `json:"bindings"`
	BindingsTotal     string                `json:"bindings_total"`
	BindingsTruncated bool                  `json:"bindings_truncated"`
	Notices           []string              `json:"notices"`
	OwnCaptures       []attendeeCaptureItem `json:"own_captures,omitempty"`
}

type attendeeResult struct{ *listResult }
type attendeeSyncResult struct {
	Captured               int                       `json:"captured"`
	Reused                 int                       `json:"reused"`
	Skipped                int                       `json:"skipped"`
	Failed                 int                       `json:"failed"`
	NativeAttendeeEvidence *AttendeeEvidenceCoverage `json:"native_attendee_evidence"`
	meta
}

var runAttendeeSync = syncer.SyncAttendees
var openAttendeeRead = store.OpenReadOnly

func attendeeSourceSelector(value *int64) (int64, error) {
	if value == nil {
		return 0, nil
	}
	if *value <= 0 {
		return 0, errs.Usage("--source-id must be a positive admitted archive int64 ID")
	}
	return *value, nil
}

func attendeeID(value string) (int64, error) {
	raw, ok := strings.CutPrefix(value, "attendee-observation:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !ok || err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, errs.Usage("show requires a positive canonical attendee-observation:ID from list")
	}
	return id, nil
}

func attendeeDecimal(value *int64) *string {
	if value == nil {
		return nil
	}
	text := strconv.FormatInt(*value, 10)
	return &text
}

func shapeAttendeeCapture(id int64, db string) (attendeeCaptureItem, bool) {
	if id <= 0 {
		return attendeeCaptureItem{}, false
	}
	query := fmt.Sprintf("SELECT * FROM outlook_attendee_snapshots WHERE rowid=%d", id)
	return attendeeCaptureItem{SnapshotID: strconv.FormatInt(id, 10), FullCaptureSQL: query,
		FullCaptureCommand: archiveScopedCommand(db, "--max-text", "0", "sql", query)}, true
}

func attendeeFullSQL(id int64) string {
	return fmt.Sprintf(`SELECT o.*,
 json_extract(s.result_json,'$.attendees['||o.ordinal||']') AS native_observation,
 json_extract(s.result_json,'$.losses') AS source_losses,
 EXISTS(SELECT 1 FROM json_each(s.result_json,'$.ambiguous_keys') a
        WHERE a.value=o.native_key) AS ambiguous_native_key,
 s.first_attempt_id,s.representation_digest,s.qualification,s.acquired_at,s.source_observed_at
FROM outlook_attendee_occurrences o
JOIN outlook_attendee_snapshots s ON s.rowid=o.snapshot_id
WHERE o.rowid=%d`, id)
}

func shapeAttendee(row store.AttendeeRow, maxText int, db string) AttendeeEvidenceItem {
	var name, email *string
	cut := false
	if row.Native.Name != nil {
		text, clipped := truncateRunes(*row.Native.Name, maxText)
		name = &text
		cut = clipped
	}
	if row.Native.Email != nil {
		text := *row.Native.Email
		email = &text
	}
	losses := []attendeeLoss{}
	for _, loss := range row.Losses {
		losses = append(losses, attendeeLoss{loss.Code, strconv.Itoa(loss.Count)})
	}
	query := attendeeFullSQL(row.ID)
	return AttendeeEvidenceItem{ID: "attendee-observation:" + strconv.FormatInt(row.ID, 10), SourceID: strconv.FormatInt(row.SourceID, 10),
		SnapshotID: strconv.FormatInt(row.SnapshotID, 10), CaptureID: strconv.FormatInt(row.CaptureID, 10), RepresentationDigest: row.RepresentationDigest,
		Qualification: row.Qualification, Ordinal: row.Ordinal, NativeKey: row.Native.Key, DetailKey: row.Native.DetailKey, VersionRaw: strconv.FormatUint(row.Native.VersionRaw, 10),
		BlockOffset: strconv.FormatInt(row.Native.BlockOffset, 10), PayloadPos: strconv.Itoa(row.Native.PayloadPos), Name: name, Email: email, Resynced: row.Native.Resynced,
		Ambiguous: row.Ambiguous, Retained: row.Retained, AcquiredAt: row.AcquiredAt, SourceObservedAt: row.SourceObservedAt, SourceLosses: losses,
		LossScope: "original_snapshot_not_per_row", Scope: store.AttendeeScope, ScopeState: row.ScopeState, TextTruncated: cut,
		FullRecordSQL: query, FullRecordCommand: archiveScopedCommand(db, "--max-text", "0", "sql", query)}
}

func (rt *runtime) shapeAttendeeItems(items []any) []any {
	projection := *rt
	if contains(rt.fields, "name") && !contains(rt.fields, "text_truncated") {
		projection.fields = append(append([]string{}, rt.fields...), "text_truncated")
	}
	return shape(&projection, items)
}

func emptyAttendeeCoverage() *AttendeeEvidenceCoverage {
	return &AttendeeEvidenceCoverage{Scope: store.AttendeeScope, Sources: []attendeeSourceItem{}, Bindings: []attendeeBindingItem{}, BindingsTotal: "0",
		Notices: []string{"Reader-selected source detail is not the retained/unioned displayed attendee roster or current membership.",
			"Archived read/origin/link matches do not certify live application state; source losses belong to their original snapshot, not each returned row.",
			"Current reads audit relevant current candidates, not unrelated undisplayed historical bodies; --history and show audit their own relevant retained evidence."}}
}

func (rt *runtime) attendeeCoverage(snapshot store.AttendeeSnapshot) *AttendeeEvidenceCoverage {
	out := emptyAttendeeCoverage()
	out.EventID, out.BindingsTotal, out.BindingsTruncated = snapshot.EventID, strconv.FormatInt(snapshot.BindingsTotal, 10), snapshot.BindingsTruncated
	for _, source := range snapshot.Coverage.Sources {
		item := attendeeSourceItem{RegistrationID: strconv.FormatInt(source.RegistrationID, 10), SourceID: attendeeDecimal(source.SourceID), AttemptID: attendeeDecimal(source.AttemptID),
			AdmittedID: attendeeDecimal(source.AdmittedID), SnapshotID: attendeeDecimal(source.SnapshotID), CalendarReadID: attendeeDecimal(source.CalendarReadID),
			LinkRevision: attendeeDecimal(source.LinkRevision), CurrentReadID: attendeeDecimal(source.CurrentReadID), CurrentLinkRevision: attendeeDecimal(source.CurrentLinkRevision),
			Locator: source.Locator, Account: source.Account, Status: source.Status, Reason: source.Reason, RepresentationDigest: source.RepresentationDigest, Qualification: source.Qualification,
			OriginState: source.OriginState, CalendarState: source.CalendarState, LinkState: source.LinkState, ScopeState: source.ScopeState,
			CapturedLink:   peopleLinkItem{source.CapturedLink.ProfileAccount, source.CapturedLink.Principal, source.CapturedLink.Method, source.CapturedLink.LinkedAt},
			CurrentLink:    peopleLinkItem{source.CurrentLink.ProfileAccount, source.CurrentLink.Principal, source.CurrentLink.Method, source.CurrentLink.LinkedAt},
			AttendeesTotal: strconv.FormatInt(source.AttendeesTotal, 10), ParentsTotal: strconv.FormatInt(source.ParentsTotal, 10), BindingsTotal: strconv.FormatInt(source.BindingsTotal, 10),
			LossScope: "original_admitted_snapshot_not_per_row", Losses: []attendeeLoss{}, AttemptedAt: source.AttemptedAt, AdmittedAt: source.AdmittedAt, AcquiredAt: source.AcquiredAt, SourceObservedAt: source.SourceObservedAt}
		for _, loss := range source.Losses {
			item.Losses = append(item.Losses, attendeeLoss{loss.Code, strconv.Itoa(loss.Count)})
		}
		if source.SourceID != nil {
			item.Retry = rt.archiveCommand("calendar", "attendees", "sync", "--source-id", strconv.FormatInt(*source.SourceID, 10))
		} else {
			item.Retry = rt.archiveCommand("calendar", "attendees", "sync", "--root", source.Request.Root)
		}
		if source.SnapshotID != nil {
			capture, _ := shapeAttendeeCapture(*source.SnapshotID, rt.archiveRef())
			item.FullCaptureSQL, item.FullCaptureCommand = capture.FullCaptureSQL, capture.FullCaptureCommand
		}
		out.Sources = append(out.Sources, item)
	}
	for _, binding := range snapshot.Bindings {
		selection := binding.Selection
		out.Bindings = append(out.Bindings, attendeeBindingItem{ID: strconv.FormatInt(binding.ID, 10), SourceID: strconv.FormatInt(binding.SourceID, 10), SnapshotID: strconv.FormatInt(binding.SnapshotID, 10),
			NativeEventID: selection.SourceID, DetailKey: selection.DetailKey, LastModifiedRaw: strconv.FormatUint(selection.LastModifiedRaw, 10), StampRaw: strconv.FormatUint(selection.StampRaw, 10),
			BlockOffset: strconv.FormatInt(selection.BlockOffset, 10), PayloadPos: strconv.Itoa(selection.PayloadPos), DetailCopies: strconv.Itoa(selection.DetailCopies),
			DetailResyncedCopies: strconv.Itoa(selection.DetailResyncedCopies), DetailPresent: selection.DetailPresent, DetailDiffering: selection.DetailDiffering,
			Retained: binding.Retained, MatchesRead: binding.MatchesRead, MatchesOrigin: binding.MatchesOrigin, MatchesLink: binding.MatchesLink, CalendarRowPresent: binding.CalendarRowPresent})
	}
	seen := map[int64]bool{}
	addOwn := func(id int64) {
		// At most100 physical rows plus100 bindings are previewed by the store.
		if seen[id] || len(out.OwnCaptures) >= 200 {
			return
		}
		if capture, valid := shapeAttendeeCapture(id, rt.archiveRef()); valid {
			seen[id] = true
			out.OwnCaptures = append(out.OwnCaptures, capture)
		}
	}
	for _, row := range snapshot.Rows {
		addOwn(row.SnapshotID)
	}
	for _, binding := range snapshot.Bindings {
		addOwn(binding.SnapshotID)
	}
	return out
}

func (rt *runtime) attendeeError(err error) error {
	switch {
	case errors.Is(err, store.ErrAttendeeArchiveCorrupt):
		return &errs.Coded{Code: "outlook_attendee_archive_corrupt", Exit: errs.ExitEnvironment, Message: "archived native attendee evidence is inconsistent; no projection was returned",
			Fix: "Preserve the selected archive for diagnosis. Native reads never migrate or repair it."}
	case errors.Is(err, store.ErrKnowledgeSourceUnknown), errors.Is(err, sql.ErrNoRows), errors.Is(err, store.ErrNoArchive):
		return &errs.Coded{Code: "outlook_attendee_unknown", Exit: errs.ExitUsage, Message: "the requested archived event, source or native observation is unavailable",
			Fix: "Read `" + rt.archiveCommand("calendar", "attendees", "sources") + "`; use an event ID from the selected archive."}
	default:
		return asCoded(err)
	}
}

func (rt *runtime) attendeeRead(label string, read func(*store.Store) (result, error)) error {
	if !filepath.IsAbs(rt.archiveRef()) {
		return errs.Usage("the selected archive must resolve to an absolute path")
	}
	s, err := openAttendeeRead(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		s = nil
	} else if err != nil {
		return rt.attendeeError(err)
	}
	res, readErr := read(s)
	if s != nil {
		readErr = errors.Join(readErr, s.Close())
	}
	if readErr != nil {
		return rt.attendeeError(readErr)
	}
	return rt.write(label, res)
}

func (c *attendeeListCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "calendar attendees list", ""); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	id, err := attendeeSourceSelector(c.SourceID)
	if err != nil {
		return err
	}
	return rt.attendeeRead("calendar attendees list", func(s *store.Store) (result, error) {
		var snapshot store.AttendeeSnapshot
		if s != nil {
			var err error
			snapshot, err = s.AttendeeEventEvidence(rt.ctx, rt.account, c.Event, store.AttendeeFilter{SourceID: id, Limit: c.Limit, History: c.History})
			if err != nil {
				return nil, err
			}
		}
		out := newList(nil, snapshot.Truncated)
		out.sourceOnly = true
		for _, row := range snapshot.Rows {
			out.Items = append(out.Items, shapeAttendee(row, rt.g.MaxText, rt.archiveRef()))
		}
		out.Count = len(out.Items)
		out.Total = int(snapshot.Total)
		out.Items = rt.shapeAttendeeItems(out.Items)
		coverage := rt.attendeeCoverage(snapshot)
		out.NativeAttendeeEvidence = coverage
		return &attendeeResult{out}, nil
	})
}

func (c *attendeeShowCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "calendar attendees show", ""); err != nil {
		return err
	}
	id, err := attendeeID(c.ID)
	if err != nil {
		return err
	}
	return rt.attendeeRead("calendar attendees show", func(s *store.Store) (result, error) {
		if s == nil {
			return nil, store.ErrKnowledgeSourceUnknown
		}
		detail, err := s.AttendeeDetail(rt.ctx, rt.account, id)
		if err != nil {
			return nil, err
		}
		coverage := rt.attendeeCoverage(store.AttendeeSnapshot{Coverage: detail.Coverage, Rows: []store.AttendeeRow{detail.Row}, Bindings: detail.Bindings, BindingsTotal: detail.BindingsTotal, BindingsTruncated: detail.BindingsTruncated})
		out := newList(rt.shapeAttendeeItems([]any{shapeAttendee(detail.Row, rt.g.MaxText, rt.archiveRef())}), false)
		out.sourceOnly = true
		out.NativeAttendeeEvidence = coverage
		return &attendeeResult{out}, nil
	})
}

func (c *attendeeSourcesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "calendar attendees sources", ""); err != nil {
		return err
	}
	id, err := attendeeSourceSelector(c.SourceID)
	if err != nil {
		return err
	}
	return rt.attendeeRead("calendar attendees sources", func(s *store.Store) (result, error) {
		var snapshot store.AttendeeSnapshot
		if s != nil {
			var err error
			snapshot, err = s.AttendeeSources(rt.ctx, id)
			if err != nil {
				return nil, err
			}
		}
		coverage := rt.attendeeCoverage(snapshot)
		out := newList(shape(rt, coverage.Sources), false)
		out.sourceOnly = true
		out.NativeAttendeeEvidence = coverage
		return &attendeeResult{out}, nil
	})
}

func (c *attendeeSyncCmd) Run(rt *runtime) error {
	if err := checkKeys(rt, nil); err != nil {
		return err
	}
	id, err := attendeeSourceSelector(c.SourceID)
	if err != nil {
		return err
	}
	if id > 0 && c.Root != "" {
		return errs.Usage("--root and --source-id are mutually exclusive")
	}
	if rt.g.Account != "" {
		return errs.Usage("attendee sync captures source evidence, not subject account ownership; drop --account")
	}
	if !filepath.IsAbs(rt.archiveRef()) {
		return errs.Usage("the selected archive must resolve to an absolute path")
	}
	report, err := runAttendeeSync(rt.ctx, syncer.AttendeeOptions{DBPath: rt.dbPath, Root: c.Root, SourceID: id, Explicit: true, Discover: id == 0})
	if err != nil {
		failure := rt.attendeeError(err)
		var coded *errs.Coded
		if errors.As(failure, &coded) {
			copy := *coded
			args := []string{"calendar", "attendees", "sync"}
			if id > 0 {
				args = append(args, "--source-id", strconv.FormatInt(id, 10))
			} else if c.Root != "" {
				args = append(args, "--root", c.Root)
			}
			copy.Fix = "Inspect `" + rt.archiveCommand("calendar", "attendees", "sources") + "`; retry only `" + rt.archiveCommand(args...) + "` after resolving the reported failure. No automatic archive repair or unrelated source fetch."
			if copy.Code == "outlook_attendee_origin_unknown" {
				root := c.Root
				var bootstrap *syncer.AttendeeBootstrapRequired
				if errors.As(err, &bootstrap) {
					root = bootstrap.Root
				}
				if root != "" {
					ordinary := []string{"sync", "--outlook-root", root}
					if rt.g.TeamsRoot != "" {
						ordinary = append(ordinary, "--teams-root", rt.g.TeamsRoot)
					}
					copy.Fix = "First verify this requested root is the original calendar source, not a same-name foreign copy. Bootstrap only through an eligible ordinary decode with `" +
						rt.archiveCommand(ordinary...) + "` (normal configured Teams capture also applies; copy interval and unchanged checks remain). Then retry `" +
						rt.archiveCommand(args...) + "`. Standalone sync never claims an existing unknown origin."
				}
			}
			return errors.Join(&copy, failure)
		}
		return failure
	}
	return rt.attendeeRead("calendar attendees sync", func(s *store.Store) (result, error) {
		var snapshot store.AttendeeSnapshot
		if s != nil {
			var err error
			snapshot, err = s.AttendeeSources(rt.ctx, id)
			if err != nil {
				return nil, err
			}
		}
		coverage := rt.attendeeCoverage(snapshot)
		return &attendeeSyncResult{Captured: report.Captured, Reused: report.Reused, Skipped: report.Skipped, Failed: report.Failed, NativeAttendeeEvidence: coverage}, nil
	})
}

func (r *attendeeResult) renderAttendees(rt *runtime) error {
	if _, err := fmt.Fprintf(rt.stdout, "Native attendee observations (not current membership): %d shown; truncated=%t\n", r.Count, r.Truncated); err != nil {
		return err
	}
	encoder := json.NewEncoder(rt.stdout)
	encoder.SetEscapeHTML(false)
	for _, item := range r.Items {
		if err := encoder.Encode(item); err != nil {
			return err
		}
	}
	return rt.renderAttendeeCoverage(r.NativeAttendeeEvidence)
}

func (r *attendeeSyncResult) renderAttendees(rt *runtime) error {
	if _, err := fmt.Fprintf(rt.stdout, "Native attendee sync: %d captured, %d reused, %d skipped, %d failed\n", r.Captured, r.Reused, r.Skipped, r.Failed); err != nil {
		return err
	}
	return rt.renderAttendeeCoverage(r.NativeAttendeeEvidence)
}

func (rt *runtime) renderAttendeeCoverage(coverage *AttendeeEvidenceCoverage) error {
	if coverage == nil {
		return nil
	}
	if _, err := fmt.Fprintf(rt.stdout, "Native attendee evidence: %s; bindings %d of %s (truncated=%t)\n", coverage.Scope, len(coverage.Bindings), coverage.BindingsTotal, coverage.BindingsTruncated); err != nil {
		return err
	}
	for _, source := range coverage.Sources {
		if _, err := fmt.Fprintf(rt.stdout, "  registration %s: %s; origin=%s calendar=%s link=%s scope=%s; reason=%s\n  retry: %s\n", source.RegistrationID, source.Status, source.OriginState, source.CalendarState, source.LinkState, source.ScopeState, source.Reason, source.Retry); err != nil {
			return err
		}
		if source.FullCaptureCommand != "" {
			if _, err := fmt.Fprintf(rt.stdout, "  full capture: %s\n", source.FullCaptureCommand); err != nil {
				return err
			}
		}
	}
	for _, capture := range coverage.OwnCaptures {
		if _, err := fmt.Fprintf(rt.stdout, "  own snapshot %s full capture: %s\n", capture.SnapshotID, capture.FullCaptureCommand); err != nil {
			return err
		}
	}
	for _, binding := range coverage.Bindings {
		if _, err := fmt.Fprintf(rt.stdout, "  binding %s: native event %s -> detail %d; read=%t origin=%t link=%t row_present=%t retained=%t\n",
			binding.ID, binding.NativeEventID, binding.DetailKey, binding.MatchesRead, binding.MatchesOrigin, binding.MatchesLink, binding.CalendarRowPresent, binding.Retained); err != nil {
			return err
		}
	}
	for _, notice := range coverage.Notices {
		if _, err := fmt.Fprintln(rt.stdout, "notice:", notice); err != nil {
			return err
		}
	}
	return nil
}

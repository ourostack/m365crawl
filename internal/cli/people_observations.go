package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

type peopleGroup struct {
	List    peopleCmd            `cmd:"" default:"withargs" hidden:"" help:"List people and source-qualified observations."`
	Sync    peopleSyncCmd        `cmd:"" help:"Register and capture native Outlook people observations from supported HxStore profiles."`
	Show    peopleObservationCmd `cmd:"" help:"Inspect one native person observation, retained copies, contradictions and unresolved scope."`
	Sources peopleSourcesCmd     `cmd:"" help:"Show registered native people sources and their independent coverage."`
}

func (peopleGroup) Help() string {
	return "Without a subcommand, list existing Teams people and mail correspondents plus registered native Outlook observations; an unscoped list says in a notice what it included.\n" +
		"last_seen_at (the time column) is the newest time the person appears in archived data: sent or was mentioned in a Teams message, was a member of a chat at its last message, or sent or received archived mail. It is not the last chat with you: for chat recency use `conversations` (last_message_at) or `messages --conversation <chat>`.\n" +
		"IDs: 8:orgid:<guid> is a person; 28:<id> is a bot or app; 19:<id>@thread... is a chat, channel or team named in a mention; everyone is the @everyone mention; mail:<address> is a mail correspondent; person-observation:ID is a native Outlook observation. " +
		"--account takes <tenant_id>/<user_id> as `m365crawl whoami` (or `status`) lists the accounts; it narrows Teams people to that account's tenant, does not narrow mail correspondents, and leaves native observations out.\n" +
		"Sort: known last_seen_at newest first, then unknown times; ties use display name (ASCII case-insensitive), then source and observation ID for native rows, otherwise person ID. Native copies are evidence, not current contacts: names, email pairs and profile-owner links do not prove subject tenancy or last contact. No native identities are merged into existing people.\n" +
		"Run `people sync` for available Mac HxStore profiles, or `people sync --root DIR` for supported copied <root>/<profile>/HxStore.hxd inputs. Windows OWA people discovery is unsupported. Ordinary reads refresh registered sources only; --no-sync performs no capture. Partial and failed capture retains observations and contradictions.\n" +
		"Follow person-observation:ID with `people show`; existing Teams/mail IDs retain their previous workflows. Profile link evidence is not a resolved person. Automatic copies respect the five-minute HxStore minimum interval."
}

type peopleObservationItem struct {
	ID               string     `json:"id"`
	DisplayName      string     `json:"display_name"`
	Email            string     `json:"email"`
	Sources          []string   `json:"sources"`
	SourceID         int64      `json:"source_id"`
	CaptureID        int64      `json:"capture_id"`
	NativeClass      uint16     `json:"native_class"`
	NativeKey        uint32     `json:"native_key"`
	NativeParent     uint32     `json:"native_parent"`
	VersionRaw       uint64     `json:"version_raw"`
	Resynced         bool       `json:"resynced"`
	FirstName        string     `json:"first_name"`
	LastName         string     `json:"last_name"`
	AlternateEmail   string     `json:"alternate_email"`
	TeamsID          string     `json:"teams_id"`
	RefreshedAt      *time.Time `json:"refreshed_at"`
	CapturedAt       *time.Time `json:"captured_at"`
	SourceObservedAt *time.Time `json:"source_observed_at"`
	Retained         bool       `json:"retained"`
	Ambiguous        bool       `json:"ambiguous"`
	Conflicting      bool       `json:"conflicting"`
	IdentityState    string     `json:"identity_state"`
	TextTruncated    bool       `json:"text_truncated,omitempty"`
}

type peopleLinkItem struct {
	ProfileAccount string     `json:"profile_account"`
	Principal      string     `json:"principal"`
	Method         string     `json:"method"`
	LinkedAt       *time.Time `json:"linked_at"`
}

type peopleDetailItem struct {
	peopleObservationItem
	Copies       []peopleObservationItem `json:"copies"`
	Conflicts    []peopleObservationItem `json:"conflicts"`
	CapturedLink peopleLinkItem          `json:"captured_profile_link"`
	CurrentLink  peopleLinkItem          `json:"current_profile_link"`
	Explanation  string                  `json:"explanation"`
}

func peopleKeys() []string {
	return dedupe(append(typeKeys[personItem](), typeKeys[peopleObservationItem]()...))
}

func peopleDetailKeys() []string {
	return append(typeKeys[peopleObservationItem](), typeKeys[peopleDetailItem]()...)
}

func peopleItem(row store.PeopleObservationRow, maxText int) peopleObservationItem {
	p := row.Person
	var refreshed *time.Time
	if !p.RefreshedAt.IsZero() {
		at := p.RefreshedAt
		refreshed = &at
	}
	display, displayCut := truncateRunes(p.DisplayName, maxText)
	first, firstCut := truncateRunes(p.FirstName, maxText)
	last, lastCut := truncateRunes(p.LastName, maxText)
	return peopleObservationItem{ID: fmt.Sprintf("person-observation:%d", row.ID), DisplayName: display, Email: p.Email,
		Sources: []string{"outlook_people"}, SourceID: row.SourceID, CaptureID: row.CaptureID, NativeClass: p.Class, NativeKey: p.Key,
		NativeParent: p.Parent, VersionRaw: p.VersionRaw, Resynced: p.Resynced, FirstName: first, LastName: last,
		AlternateEmail: p.AlternateEmail, TeamsID: p.TeamsID, RefreshedAt: refreshed, CapturedAt: row.CapturedAt,
		SourceObservedAt: row.SourceObservedAt, Retained: row.Retained, Ambiguous: row.Ambiguous, Conflicting: row.Conflicting,
		IdentityState: "unresolved", TextTruncated: displayCut || firstCut || lastCut}
}

func peopleCoverage(sources []store.KnowledgeSource) *noteCoverage {
	coverage := notesCoverage(sources)
	coverage.Scope = "outlook_native_people_observations"
	return coverage
}

var runPeopleSync = syncer.SyncPeople
var openPeopleRead = store.OpenReadOnly

func (rt *runtime) refreshPeople() (*noteCoverage, error) {
	var report syncer.PeopleReport
	refreshErr := rt.besideOtherRun(func() (err error) {
		report, err = runPeopleSync(rt.ctx, syncer.PeopleOptions{DBPath: rt.dbPath, MaxAge: rt.maxAge})
		return err
	})
	if refreshErr == nil {
		return peopleCoverage(report.Sources), nil
	}
	if errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return nil, refreshErr
	}
	s, err := openPeopleRead(rt.ctx, rt.dbPath)
	if err != nil {
		return nil, errors.Join(refreshErr, err)
	}
	sources, readErr := s.KnowledgeSources(rt.ctx, "outlook_people")
	if err := errors.Join(readErr, s.Close()); err != nil {
		return nil, errors.Join(refreshErr, err)
	}
	coverage := peopleCoverage(sources)
	coded := bodyOf(errs.DBError(refreshErr))
	coverage.RefreshError = &syncError{Code: coded.Code, Message: coded.Message}
	return coverage, nil
}

func (c *peopleCmd) runConnectedPeople(rt *runtime, coverage *noteCoverage) error {
	s, err := openPeopleRead(rt.ctx, rt.dbPath)
	if err != nil {
		return asCoded(err)
	}
	// A native-only archive does not invoke unrelated legacy freshness.
	legacy, _, err := s.People(rt.ctx, "", store.Filter{Limit: 1})
	closeErr := s.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return asCoded(err)
	}
	read := func(s *store.Store) (result, error) { return c.connectedPeopleResult(rt, s, coverage) }
	if len(legacy) > 0 {
		rt.query = listQuery{filtered: c.Query != ""}
		return rt.read("people", read)
	}
	s, err = openPeopleRead(rt.ctx, rt.dbPath)
	if err != nil {
		return asCoded(err)
	}
	res, readErr := c.connectedPeopleResult(rt, s, coverage)
	if err := errors.Join(readErr, s.Close()); err != nil {
		return asCoded(err)
	}
	res.sourceOnly = true
	return rt.write("people", res)
}

func (c *peopleCmd) connectedPeopleResult(rt *runtime, s *store.Store, coverage *noteCoverage) (*listResult, error) {
	rows, truncated, err := s.People(rt.ctx, c.Query, store.Filter{Account: rt.account, Limit: c.Limit})
	if err != nil {
		return nil, err
	}
	type ordered struct {
		item               any
		at                 time.Time
		name, id           string
		sourceID, nativeID int64
	}
	all := []ordered{}
	for _, row := range rows {
		all = append(all, ordered{item: personItem{TenantID: row.TenantID, ID: row.ID, DisplayName: row.DisplayName,
			Email: row.Email, Sources: row.Sources, LastSeenAt: row.LastSeenAt}, at: row.LastSeenAt, name: row.DisplayName, id: row.ID})
	}
	native := []store.PeopleObservationRow{}
	if rt.g.Account == "" {
		var more bool
		native, more, err = s.PeopleObservations(rt.ctx, c.Query, 0, c.Limit)
		if err != nil {
			return nil, err
		}
		truncated = truncated || more
	}
	for _, row := range native {
		item := peopleItem(row, rt.g.MaxText)
		all = append(all, ordered{item: item, name: row.Person.DisplayName, id: item.ID, sourceID: row.SourceID, nativeID: row.ID})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].at.Equal(all[j].at) {
			return all[i].at.After(all[j].at)
		}
		a, b := asciiFold(all[i].name), asciiFold(all[j].name)
		if a != b {
			return a < b
		}
		if all[i].nativeID != 0 && all[j].nativeID != 0 {
			if all[i].sourceID != all[j].sourceID {
				return all[i].sourceID < all[j].sourceID
			}
			return all[i].nativeID < all[j].nativeID
		}
		return all[i].id < all[j].id
	})
	if len(all) > c.Limit {
		all = all[:c.Limit]
		truncated = true
	}
	items := make([]any, len(all))
	for i, row := range all {
		items[i] = row.item
	}
	out := newList(shape(rt, items), truncated)
	sources, err := s.KnowledgeSources(rt.ctx, "outlook_people")
	if err != nil {
		return nil, err
	}
	out.SourceCoverage = peopleCoverage(sources)
	out.SourceCoverage.RefreshError = coverage.RefreshError
	if len(items) == 0 {
		out.Note = "no archived person matched; source_coverage describes captured native input, not contact completeness"
	}
	if rt.g.Account != "" {
		out.addNotice("native Outlook subjects have unresolved account ownership and were excluded; profile-owner links do not prove subject tenancy")
	} else {
		out.addNotice(peopleIncludedNotice(len(sources)))
	}
	return out, nil
}

// peopleIncludedNotice says what an unscoped people list mixed in and the ownership caveat.
func peopleIncludedNotice(nativeSources int) string {
	if nativeSources == 0 {
		return "included Teams people and mail correspondents of every account; no native Outlook people sources are registered (m365crawl people sync adds them)"
	}
	return "included Teams people and mail correspondents of every account, plus native Outlook people observations (person-observation:ID) from " +
		plural(nativeSources, "registered source", "registered sources") + "; native Outlook subjects have unresolved account ownership, so profile-owner links do not prove whose contact they are, and --account leaves them out"
}

func asciiFold(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}

func (rt *runtime) peopleRead(label string, fn func(*store.Store) (*listResult, error)) error {
	if rt.g.Account != "" {
		return errs.Usage("native people observations do not establish subject tenancy; drop --account")
	}
	coverage, err := rt.refreshPeople()
	if err != nil {
		return asCoded(err)
	}
	s, err := openPeopleRead(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		s = nil
	} else if err != nil {
		return asCoded(err)
	}
	res, readErr := fn(s)
	if s != nil {
		readErr = errors.Join(readErr, s.Close())
	}
	if readErr != nil {
		return asCoded(readErr)
	}
	res.SourceCoverage, res.sourceOnly = coverage, true
	return rt.write(label, res)
}

type peopleSyncCmd struct {
	Root string `help:"Supported copied HxStore profile root; default: Mac Outlook profiles."`
}

func (c *peopleSyncCmd) Run(rt *runtime) error {
	if rt.g.Account != "" {
		return errs.Usage("people sync records native observations, not subject account ownership; drop --account")
	}
	report, err := runPeopleSync(rt.ctx, syncer.PeopleOptions{DBPath: rt.dbPath, Root: c.Root, Explicit: true, Discover: true})
	if err != nil {
		if errors.Is(err, outlookdesktop.ErrNotSupported) {
			return &errs.Coded{Code: "outlook_people_source_unsupported", Exit: errs.ExitEnvironment,
				Message: "default native people discovery is supported for Mac HxStore profiles only",
				Fix:     "Use `people sync --root DIR` with a supported copied <root>/<profile>/HxStore.hxd input; Windows OWA people discovery is not implemented."}
		}
		return asCoded(err)
	}
	return rt.write("people sync", notesSyncResult{Captured: report.Captured, Unchanged: report.Unchanged,
		Skipped: report.Skipped, Failed: report.Failed, SourceCoverage: peopleCoverage(report.Sources)})
}

type peopleObservationCmd struct {
	ID string `arg:"" help:"Stable person-observation:ID; existing Teams/mail IDs use their original workflows."`
}

func (c *peopleObservationCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "people show", ""); err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(c.ID, "person-observation:"), 10, 64)
	if err != nil || id <= 0 || !strings.HasPrefix(c.ID, "person-observation:") {
		return errs.Usage("people show requires person-observation:ID from the people list; Teams/mail IDs retain their existing workflows")
	}
	return rt.peopleRead("people show", func(s *store.Store) (*listResult, error) {
		if s == nil {
			return nil, errs.Usage("person observation not found; run people sync")
		}
		row, err := s.PeopleObservation(rt.ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errs.Usage("person observation not found")
		}
		if err != nil {
			return nil, err
		}
		link := func(v store.PeopleCaptureLink) peopleLinkItem {
			return peopleLinkItem{ProfileAccount: v.ProfileAccount, Principal: v.Principal, Method: v.Method, LinkedAt: v.LinkedAt}
		}
		item := peopleDetailItem{peopleObservationItem: peopleItem(row.PeopleObservationRow, rt.g.MaxText), Copies: []peopleObservationItem{},
			Conflicts: []peopleObservationItem{}, CapturedLink: link(row.CapturedLink), CurrentLink: link(row.CurrentLink),
			Explanation: "Native copies and email/directory pairs are historical source claims. Profile-owner links do not establish subject tenancy, current contacts or last contact. Conflicts remain unresolved."}
		for _, copy := range row.Copies {
			item.Copies = append(item.Copies, peopleItem(copy, rt.g.MaxText))
		}
		for _, conflict := range row.Conflicts {
			item.Conflicts = append(item.Conflicts, peopleItem(conflict, rt.g.MaxText))
		}
		return newList(shape(rt, []peopleDetailItem{item}), false), nil
	})
}

type peopleSourcesCmd struct{}

func (*peopleSourcesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "people sources", ""); err != nil {
		return err
	}
	return rt.peopleRead("people sources", func(s *store.Store) (*listResult, error) {
		if s == nil {
			return newList(nil, false), nil
		}
		sources, err := s.KnowledgeSources(rt.ctx, "outlook_people")
		if err != nil {
			return nil, err
		}
		return newList(shape(rt, peopleCoverage(sources).Sources), false), nil
	})
}

func (rt *runtime) renderPeopleDetails(result *listResult) {
	for _, item := range result.Items {
		detail, ok := item.(peopleDetailItem)
		if !ok {
			continue
		}
		_, _ = fmt.Fprintf(rt.stdout, "\nidentity: %s\n%s\n", detail.IdentityState, detail.Explanation)
		for _, copy := range detail.Copies {
			_, _ = fmt.Fprintf(rt.stdout, "native copy %s: %s / %s (class%d key%d version%d; ambiguous=%t conflicting=%t)\n",
				copy.ID, copy.Email, copy.TeamsID, copy.NativeClass, copy.NativeKey, copy.VersionRaw, copy.Ambiguous, copy.Conflicting)
		}
		for _, conflict := range detail.Conflicts {
			_, _ = fmt.Fprintf(rt.stdout, "conflicting claim %s: %s / %s\n", conflict.ID, conflict.Email, conflict.TeamsID)
		}
		_, _ = fmt.Fprintf(rt.stdout, "captured profile link: %s / %s (%s)\ncurrent profile link: %s / %s (%s)\n",
			detail.CapturedLink.ProfileAccount, detail.CapturedLink.Principal, detail.CapturedLink.Method,
			detail.CurrentLink.ProfileAccount, detail.CurrentLink.Principal, detail.CurrentLink.Method)
	}
}

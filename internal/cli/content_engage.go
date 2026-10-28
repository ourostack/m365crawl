package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

type engageAccount struct {
	Host      string `json:"host"`
	NetworkID string `json:"network_id"`
	UserID    string `json:"user_id"`
}
type engageDisplayLoss struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}
type EngageContentItem struct {
	ID                   string                        `json:"id"`
	Kind                 string                        `json:"kind"`
	SourceID             int64                         `json:"source_id"`
	RegistrationID       int64                         `json:"registration_id"`
	SessionID            int64                         `json:"session_id"`
	CaptureID            int64                         `json:"capture_id"`
	RepresentationDigest string                        `json:"representation_digest"`
	Qualification        string                        `json:"qualification"`
	SourceAccount        engageAccount                 `json:"source_account"`
	Ordinal              int                           `json:"ordinal"`
	NativeThreadID       string                        `json:"native_thread_id"`
	NativeNetworkID      string                        `json:"native_network_id"`
	NativeGroupID        string                        `json:"native_group_id"`
	NativeStarterID      string                        `json:"native_starter_id"`
	NativeSenderID       *string                       `json:"native_sender_id"`
	Title                *string                       `json:"title"`
	Language             *string                       `json:"language"`
	Version              *int64                        `json:"version"`
	IsDeleted            *bool                         `json:"is_deleted"`
	IsDraft              *bool                         `json:"is_draft"`
	CreatedRaw           *string                       `json:"created_raw"`
	UpdatedRaw           *string                       `json:"updated_raw"`
	StarterCreatedRaw    *string                       `json:"starter_created_raw"`
	StarterUpdatedRaw    *string                       `json:"starter_updated_raw"`
	CreatedAt            *time.Time                    `json:"created_at"`
	UpdatedAt            *time.Time                    `json:"updated_at"`
	StarterCreatedAt     *time.Time                    `json:"starter_created_at"`
	StarterUpdatedAt     *time.Time                    `json:"starter_updated_at"`
	Blocks               []string                      `json:"blocks"`
	BlocksTotal          int                           `json:"blocks_total"`
	PreviewTruncated     bool                          `json:"preview_truncated"`
	TextTruncated        bool                          `json:"text_truncated"`
	Retained             bool                          `json:"retained"`
	SessionLosses        []engageDisplayLoss           `json:"session_losses"`
	SessionScope         string                        `json:"session_scope"`
	AcquiredAt           time.Time                     `json:"acquired_at"`
	CapturedAt           time.Time                     `json:"captured_at"`
	FullTextSQL          string                        `json:"full_text_sql"`
	FullTextCommand      string                        `json:"full_text_command"`
	Selected             *engagecontent.SelectedThread `json:"selected,omitempty"`
}
type engageSessionSummary struct {
	ID                   int64               `json:"session_id"`
	SourceID             int64               `json:"source_id"`
	CaptureID            int64               `json:"capture_id"`
	RepresentationDigest string              `json:"representation_digest"`
	Qualification        string              `json:"qualification"`
	State                string              `json:"state"`
	ThreadsTotal         int                 `json:"threads_total"`
	BlocksTotal          int                 `json:"blocks_total"`
	Losses               []engageDisplayLoss `json:"session_losses"`
	AcquiredAt           time.Time           `json:"acquired_at"`
	CapturedAt           time.Time           `json:"captured_at"`
}
type engageRegistrationItem struct {
	Kind                     string     `json:"kind"`
	RegistrationID           int64      `json:"registration_id"`
	LatestAttemptID          int64      `json:"latest_attempt_id"`
	Target                   string     `json:"target"`
	Outcome                  string     `json:"outcome"`
	Reason                   string     `json:"reason"`
	AccountState             string     `json:"account_state"`
	AttemptedAt              *time.Time `json:"attempted_at"`
	FinishedAt               *time.Time `json:"finished_at"`
	LastSuccessfulInstanceID *int64     `json:"last_successful_instance_id"`
	Retry                    string     `json:"retry"`
}
type engageSourceItem struct {
	Kind                     string               `json:"kind"`
	SourceID                 int64                `json:"source_id"`
	RegistrationID           int64                `json:"registration_id"`
	AdmittedAttemptID        int64                `json:"admitted_attempt_id"`
	CurrentSessionID         int64                `json:"current_session_id"`
	SourceAccount            engageAccount        `json:"source_account"`
	Qualification            string               `json:"qualification"`
	Coverage                 string               `json:"coverage"`
	LastGoodSessionID        *int64               `json:"last_good_session_id"`
	AdmittedAt               *time.Time           `json:"admitted_at"`
	CurrentSession           engageSessionSummary `json:"current_session"`
	IsLastSuccessfulInstance bool                 `json:"is_last_successful_instance"`
}
type EngageContentCoverage struct {
	Scope         string                   `json:"scope"`
	Registrations []engageRegistrationItem `json:"registrations"`
	Instances     []engageSourceItem       `json:"instances"`
}
type searchEngageItem struct {
	Source string `json:"source"`
	EngageContentItem
}

func engageAccountOf(a engagecontent.SourceAccount) engageAccount {
	return engageAccount{a.Host, a.NetworkID, a.UserID}
}
func engageDisplayLosses(losses []engagecontent.Loss) []engageDisplayLoss {
	out := make([]engageDisplayLoss, len(losses))
	for i, loss := range losses {
		out[i] = engageDisplayLoss{loss.Code, loss.Count}
	}
	return out
}

func (r EngageFetchReport) MarshalJSON() ([]byte, error) {
	type plain EngageFetchReport
	return json.Marshal(struct {
		plain
		SessionLosses []engageDisplayLoss `json:"session_losses"`
	}{plain(r), engageDisplayLosses(r.SessionLosses)})
}

func (r EngageSigninReport) MarshalJSON() ([]byte, error) {
	type plain EngageSigninReport
	return json.Marshal(struct {
		plain
		SourceAccount engageAccount `json:"source_account"`
	}{plain(r), engageAccountOf(r.SourceAccount)})
}
func engageCopy[T any](p *T) *T {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

// shapeEngage owns all preview memory. Raw/native evidence is never clipped.
func shapeEngage(row store.EngageObservationRow, maxText int) EngageContentItem {
	t := row.Thread
	kind, scope := store.EngageKind, engageScope
	if t.Selected != nil {
		kind, scope = store.EngageThreadKind, engageThreadScope
		t.Blocks = nil
		for _, message := range t.Selected.Messages {
			t.Blocks = append(t.Blocks, message.Blocks...)
		}
	}
	item := EngageContentItem{ID: fmt.Sprintf("engage-observation:%d", row.ID), Kind: kind,
		SourceID: row.SourceID, SessionID: row.SessionID, CaptureID: row.CaptureID,
		RepresentationDigest: row.RepresentationDigest, Qualification: row.Qualification, SourceAccount: engageAccountOf(row.Account),
		Ordinal: t.Ordinal, NativeThreadID: t.ID, NativeNetworkID: t.NetworkID, NativeGroupID: t.GroupID, NativeStarterID: t.StarterID,
		NativeSenderID: engageCopy(t.SenderID), Title: engageCopy(t.Title), Language: engageCopy(t.Language), Version: engageCopy(t.Version),
		IsDeleted: engageCopy(t.IsDeleted), IsDraft: engageCopy(t.IsDraft),
		CreatedRaw: engageCopy(t.CreatedRaw), UpdatedRaw: engageCopy(t.UpdatedRaw), StarterCreatedRaw: engageCopy(t.StarterCreatedRaw), StarterUpdatedRaw: engageCopy(t.StarterUpdatedRaw),
		CreatedAt: engageCopy(t.CreatedAt), UpdatedAt: engageCopy(t.UpdatedAt), StarterCreatedAt: engageCopy(t.StarterCreatedAt), StarterUpdatedAt: engageCopy(t.StarterUpdatedAt),
		Blocks: append([]string{}, t.Blocks[:min(100, len(t.Blocks))]...), BlocksTotal: len(t.Blocks), PreviewTruncated: len(t.Blocks) > 100,
		Retained: row.Retained, SessionLosses: engageDisplayLosses(row.SessionLosses), SessionScope: scope, AcquiredAt: row.AcquiredAt, CapturedAt: row.CapturedAt,
		FullTextSQL: fmt.Sprintf("SELECT o.*,s.* FROM engage_occurrences o JOIN engage_sessions s ON s.rowid=o.session_id WHERE o.rowid=%d", row.ID)}
	if t.Selected != nil {
		encoded, _ := json.Marshal(t.Selected)
		var selected engagecontent.SelectedThread
		_ = json.Unmarshal(encoded, &selected)
		for i := range selected.Messages {
			m := &selected.Messages[i]
			m.Blocks = append([]string{}, m.Blocks[:min(100, len(m.Blocks))]...)
			for j, value := range m.Blocks {
				var cut bool
				m.Blocks[j], cut = truncateRunes(value, maxText)
				item.TextTruncated = item.TextTruncated || cut
			}
			if m.Author.Name != nil {
				value, cut := truncateRunes(*m.Author.Name, maxText)
				m.Author.Name = &value
				item.TextTruncated = item.TextTruncated || cut
			}
			for j := range m.Attachments {
				if m.Attachments[j].Title != nil {
					value, cut := truncateRunes(*m.Attachments[j].Title, maxText)
					m.Attachments[j].Title = &value
					item.TextTruncated = item.TextTruncated || cut
				}
			}
		}
		item.Selected = &selected
	}
	if item.Title != nil {
		value, cut := truncateRunes(*item.Title, maxText)
		item.Title = &value
		item.TextTruncated = cut
	}
	for i, s := range item.Blocks {
		value, cut := truncateRunes(s, maxText)
		item.Blocks[i] = value
		item.TextTruncated = item.TextTruncated || cut
	}
	return item
}
func (rt *runtime) engageCoverageOf(snap store.EngageSnapshot) *EngageContentCoverage {
	out := &EngageContentCoverage{Scope: engageScope, Registrations: []engageRegistrationItem{}, Instances: []engageSourceItem{}}
	if len(snap.Registrations) == 0 {
		out.Scope = "not_registered"
	}
	for _, r := range snap.Registrations {
		kind, retry := "engage_registration", rt.contentCommand("fetch", "engage-home")
		if r.Kind == store.EngageThreadKind {
			kind, retry = "engage_thread_registration", rt.contentCommand("fetch", "engage-thread", r.Target)
			out.Scope = "native_engage_access_sessions"
		}
		out.Registrations = append(out.Registrations, engageRegistrationItem{kind, r.ID, r.LatestAttemptID, r.Target, r.Outcome, r.Reason, r.AccountState, engageCopy(r.AttemptedAt), engageCopy(r.FinishedAt), engageCopy(r.LastSuccessfulInstanceID), retry})
	}
	for _, s := range snap.Sources {
		c := s.CurrentSession
		summary := engageSessionSummary{c.ID, c.SourceID, c.CaptureID, c.RepresentationDigest, c.Qualification, c.State, c.ThreadsTotal, c.BlocksTotal, engageDisplayLosses(c.Losses), c.AcquiredAt, c.CapturedAt}
		kind := store.EngageKind
		if s.Qualification == store.EngageThreadQualification {
			kind = store.EngageThreadKind
			out.Scope = "native_engage_access_sessions"
		}
		out.Instances = append(out.Instances, engageSourceItem{kind, s.ID, s.RegistrationID, s.AdmittedAttemptID, s.CurrentSessionID, engageAccountOf(s.Account), s.Qualification, s.Coverage, engageCopy(s.LastGoodSessionID), engageCopy(s.AdmittedAt), summary, s.IsLastSuccessfulInstance})
	}
	return out
}
func engageObservationID(value string) (int64, error) {
	if !strings.HasPrefix(value, "engage-observation:") {
		return 0, errs.Usage("expected engage-observation:N with positive decimal int64")
	}
	// Reuse the established positive-decimal grammar, never a native identifier.
	id, err := cloudObservationID("cloud-observation:" + strings.TrimPrefix(value, "engage-observation:"))
	if err != nil {
		return 0, errs.Usage("expected engage-observation:N with positive decimal int64")
	}
	return id, nil
}
func engageBounds(raw string, parsed time.Time) *time.Time {
	if raw == "" {
		return nil
	}
	return &parsed
}
func engageHit(rt *runtime, row store.EngageObservationRow, sources []store.EngageSource, shared bool) searchHit {
	item := shapeEngage(row, rt.g.MaxText)
	item.FullTextCommand = rt.engageFullSQLCommand(item.FullTextSQL)
	for _, s := range sources {
		if s.ID == row.SourceID {
			item.RegistrationID = s.RegistrationID
		}
	}
	at := time.Time{}
	if row.Thread.StarterUpdatedAt != nil {
		at = *row.Thread.StarterUpdatedAt
	}
	label := row.Thread.ID
	if row.Thread.Title != nil {
		label = *row.Thread.Title
	}
	hit := searchHit{at: at, source: searchContent, engage: true, engageKnown: row.Thread.StarterUpdatedAt != nil, engageSessionID: row.SessionID, engageOrdinal: row.Thread.Ordinal,
		cloudLabel: label, cloudSourceID: row.SourceID, cloudObservationID: row.ID, item: item}
	if shared {
		hit.item = searchEngageItem{searchContent, item}
	}
	return hit
}
func (rt *runtime) showEngage(value string) error {
	id, err := engageObservationID(value)
	if err != nil {
		return err
	}
	return rt.contentRead("content show", func(st *store.Store) (*listResult, error) {
		if st == nil {
			return nil, store.ErrKnowledgeSourceUnknown
		}
		detail, err := st.EngageDetail(rt.ctx, id)
		if err != nil {
			return nil, err
		}
		item := shapeEngage(detail.Observation, rt.g.MaxText)
		item.FullTextCommand = rt.engageFullSQLCommand(item.FullTextSQL)
		item.RegistrationID = detail.Source.RegistrationID
		res := newList(shape(rt, []any{item}), false)
		res.EngageContentCoverage = rt.engageCoverageOf(store.EngageSnapshot{Sources: []store.EngageSource{detail.Source}, Registrations: []store.EngageRegistration{detail.Registration}})
		return res, nil
	})
}
func engageSourceLabel(id int64) string { return strconv.FormatInt(id, 10) }

func (rt *runtime) engageFullSQLCommand(query string) string {
	return "m365crawl --db " + quoteArg(rt.archiveRef()) + " --max-age 0 sql " + quoteArg(query)
}

func (rt *runtime) renderEngageCoverage(c *EngageContentCoverage) {
	if c == nil {
		return
	}
	_, _ = fmt.Fprintf(rt.stdout, "Engage: %s; partial access-session observations, not a complete feed or current thread revision\n", c.Scope)
	for _, r := range c.Registrations {
		_, _ = fmt.Fprintf(rt.stdout, "  registration %d: %s / %s; %s; last successfully observed instance %s (not active account); %s\n", r.RegistrationID, r.Outcome, r.AccountState, r.Reason, sourceLabel(r.LastSuccessfulInstanceID), r.Retry)
	}
	for _, s := range c.Instances {
		_, _ = fmt.Fprintf(rt.stdout, "  instance %d: own current archived session %d / %s; admitted %s; last-successful-instance=%t; session losses %v\n", s.SourceID, s.CurrentSessionID, s.Coverage, noteStamp(s.AdmittedAt), s.IsLastSuccessfulInstance, s.CurrentSession.Losses)
	}
}

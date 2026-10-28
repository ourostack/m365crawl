package engagecontent

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"time"
	"unicode/utf8"
)

const SelectedQualification = "native_selected_thread_observations"

type SelectedAuthor struct {
	ID         *string `json:"id"`
	Name       *string `json:"name"`
	NetworkID  *string `json:"network_id"`
	NativeType *string `json:"native_type"`
}
type SelectedAttachment struct {
	ID         string  `json:"id"`
	Title      *string `json:"title"`
	NativeType *string `json:"native_type"`
	URL        *string `json:"url"`
}
type SelectedLink struct {
	URL string `json:"url"`
}
type SelectedMessage struct {
	ID          string               `json:"id"`
	ThreadID    string               `json:"thread_id"`
	ParentID    *string              `json:"parent_id"`
	Author      SelectedAuthor       `json:"author"`
	CreatedRaw  *string              `json:"created_raw"`
	UpdatedRaw  *string              `json:"updated_raw"`
	CreatedAt   *time.Time           `json:"created_at"`
	UpdatedAt   *time.Time           `json:"updated_at"`
	Blocks      []string             `json:"blocks"`
	Links       []SelectedLink       `json:"links"`
	Attachments []SelectedAttachment `json:"attachments"`
	BodyState   string               `json:"body_state"`
}
type ReplyPage struct {
	EdgesObserved   int   `json:"edges_observed"`
	EdgesKnown      bool  `json:"edges_known"`
	TotalCount      *int  `json:"total_count"`
	HasNextPage     *bool `json:"has_next_page"`
	HasPreviousPage *bool `json:"has_previous_page"`
}
type SelectedThread struct {
	Messages []SelectedMessage `json:"messages"`
	Replies  ReplyPage         `json:"replies"`
}

var selectedJSONStrings = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

var selectedLossCodes = map[string]bool{
	"selected_optional_unmapped": true, "unsafe_value_redacted": true,
	"replies_partial": true, "entity_unmapped": true,
}

func selectedClean(value *string, v *validator) (*string, error) {
	if value == nil {
		v.losses["selected_optional_unmapped"]++
		return nil, nil
	}
	if err := v.string(*value, false, true); err != nil {
		return nil, err
	}
	text, changed := scrubSelectedText(*value)
	if changed {
		v.losses["unsafe_value_redacted"]++
	}
	return &text, nil
}

func decodeSelected(ctx context.Context, target ThreadTarget, raw scriptResult) (Result, error) {
	base, err := decode(ctx, raw)
	if err != nil {
		return Result{}, err
	}
	if len(base.Threads) == 0 {
		return Result{}, &ReadError{Code: "target_unverified"}
	}
	base.Selection, base.Qualification = &target, SelectedQualification
	v := validator{losses: map[string]int{}}
	for _, loss := range base.Losses {
		v.losses[loss.Code] = loss.Count
	}
	for i := range base.Threads {
		thread := &base.Threads[i]
		for _, value := range []string{base.Account.Host, base.Account.NetworkID, base.Account.UserID, thread.ID, thread.NetworkID, thread.GroupID, thread.StarterID} {
			if _, changed := scrubSelectedText(value); changed {
				return Result{}, &ReadError{Code: "malformed"}
			}
		}
		if thread.ID != target.ThreadID || (target.GroupID != "" && thread.GroupID != target.GroupID) {
			return Result{}, &ReadError{Code: "target_unverified"}
		}
		var selected SelectedThread
		payload := raw.Threads[i].Selected
		if len(payload) > 8388608 || !utf8.Valid(payload) || !json.Valid(payload) {
			return Result{}, &ReadError{Code: "malformed"}
		}
		for _, literal := range selectedJSONStrings.FindAll(payload, -1) {
			if !validStringScalar(literal) {
				return Result{}, &ReadError{Code: "malformed"}
			}
		}
		if json.Unmarshal(payload, &selected) != nil ||
			len(selected.Messages) == 0 || !slices.Equal(selected.Messages[0].Blocks, thread.Blocks) {
			return Result{}, &ReadError{Code: "malformed"}
		}
		if len(selected.Messages) > 128 {
			return Result{}, &ReadError{Code: "too_large"}
		}
		for j := range selected.Messages {
			message := &selected.Messages[j]
			if len(message.Blocks) > 4096 || len(message.Links) > 128 || len(message.Attachments) > 128 {
				return Result{}, &ReadError{Code: "too_large"}
			}
			for _, pointer := range []**string{&message.Author.ID, &message.Author.Name, &message.Author.NetworkID, &message.Author.NativeType} {
				if *pointer, err = selectedClean(*pointer, &v); err != nil {
					return Result{}, err
				}
			}
			for _, pair := range []struct {
				raw **string
				at  **time.Time
			}{{&message.CreatedRaw, &message.CreatedAt}, {&message.UpdatedRaw, &message.UpdatedAt}} {
				value, _ := json.Marshal(*pair.raw)
				if *pair.raw, *pair.at, err = v.clock(value); err != nil {
					return Result{}, err
				}
			}
			for k, value := range message.Blocks {
				if err := v.string(value, true, true); err != nil {
					return Result{}, err
				}
				clean, changed := scrubSelectedText(value)
				message.Blocks[k] = clean
				if changed {
					v.losses["unsafe_value_redacted"]++
				}
			}
			links := []SelectedLink{}
			for _, link := range message.Links {
				if SafeThreadLink(link.URL) {
					links = append(links, link)
				} else {
					v.losses["unsafe_value_redacted"]++
				}
			}
			message.Links = links
			if message.Attachments == nil {
				v.losses["attachments_unmapped"]++
			}
			attachments := message.Attachments[:0]
			for _, attachment := range message.Attachments {
				a := &attachment
				if _, changed := scrubSelectedText(a.ID); changed {
					v.losses["unsafe_value_redacted"]++
					continue
				}
				for _, pointer := range []**string{&a.Title, &a.NativeType} {
					if *pointer, err = selectedClean(*pointer, &v); err != nil {
						return Result{}, err
					}
				}
				if a.URL != nil && !SafeThreadLink(*a.URL) {
					a.URL = nil
					v.losses["unsafe_value_redacted"]++
				}
				attachments = append(attachments, *a)
			}
			message.Attachments = attachments
		}
		thread.Blocks = append([]string{}, selected.Messages[0].Blocks...)
		thread.Selected = &selected
		thread.SenderID = selected.Messages[0].Author.ID
		if thread.Title, err = selectedClean(thread.Title, &v); err != nil {
			return Result{}, err
		}
		if thread.Language, err = selectedClean(thread.Language, &v); err != nil {
			return Result{}, err
		}
		if !selected.Replies.EdgesKnown || selected.Replies.HasNextPage == nil || *selected.Replies.HasNextPage ||
			selected.Replies.HasPreviousPage == nil || *selected.Replies.HasPreviousPage ||
			selected.Replies.TotalCount == nil || *selected.Replies.TotalCount != selected.Replies.EdgesObserved {
			v.losses["replies_partial"]++
		}
	}
	base.Losses = nil
	for code, count := range v.losses {
		base.Losses = append(base.Losses, Loss{code, count})
	}
	sort.Slice(base.Losses, func(i, j int) bool { return base.Losses[i].Code < base.Losses[j].Code })
	if err := ValidateResult(ctx, base); err != nil {
		return Result{}, err
	}
	return base, nil
}

func validateSelected(ctx context.Context, r Result) error {
	bad := &ReadError{Code: "malformed"}
	target, err := ParseThreadURL(r.Selection.URL)
	if err != nil || target.URL != r.Selection.URL || target.ThreadID != r.Selection.ThreadID || r.Qualification != SelectedQualification {
		return bad
	}
	base := r
	base.Selection = nil
	base.Qualification = "native_home_observations"
	base.Threads = append([]ThreadObservation{}, r.Threads...)
	base.Losses = nil
	last := ""
	clockLoss, unknownClocks := 0, 0
	for _, loss := range r.Losses {
		if (!lossCodes[loss.Code] && !selectedLossCodes[loss.Code]) || loss.Code <= last || loss.Count < 1 || loss.Count > 65536 {
			return bad
		}
		last = loss.Code
		if loss.Code == "clock_unmapped" {
			clockLoss = loss.Count
		}
		if lossCodes[loss.Code] {
			base.Losses = append(base.Losses, loss)
		}
	}
	for i := range base.Threads {
		base.Threads[i].Selected = nil
	}
	if err := ValidateResult(ctx, base); err != nil {
		return err
	}
	if len(r.Threads) == 0 {
		return bad
	}
	v := validator{}
	messages, blocks := 0, 0
	for _, thread := range r.Threads {
		for _, value := range []string{r.Account.Host, r.Account.NetworkID, r.Account.UserID, thread.ID, thread.NetworkID, thread.GroupID, thread.StarterID, r.Selection.GroupID} {
			if _, changed := scrubSelectedText(value); changed {
				return bad
			}
		}
		for _, value := range []*string{thread.Title, thread.Language, thread.SenderID, thread.CreatedRaw, thread.UpdatedRaw, thread.StarterCreatedRaw, thread.StarterUpdatedRaw} {
			if value != nil {
				if clean, _ := scrubSelectedText(*value); clean != *value {
					return bad
				}
			}
		}
		for _, clock := range []*time.Time{thread.CreatedAt, thread.UpdatedAt, thread.StarterCreatedAt, thread.StarterUpdatedAt} {
			if clock == nil {
				unknownClocks++
			}
		}
		if thread.ID != r.Selection.ThreadID || (r.Selection.GroupID != "" && thread.GroupID != r.Selection.GroupID) || thread.Selected == nil {
			return bad
		}
		s := thread.Selected
		if len(s.Messages) == 0 || s.Messages[0].ID != thread.StarterID || !slices.Equal(s.Messages[0].Blocks, thread.Blocks) ||
			s.Replies.EdgesObserved != len(s.Messages)-1 || (s.Replies.TotalCount != nil && *s.Replies.TotalCount < 0) {
			return bad
		}
		for i, m := range s.Messages {
			if err := ctx.Err(); err != nil {
				return err
			}
			messages++
			blocks += len(m.Blocks)
			if messages > 128 || len(m.Blocks) > 4096 || blocks > 65536 || len(m.Links) > 128 || len(m.Attachments) > 128 {
				return &ReadError{Code: "too_large"}
			}
			if m.ThreadID != thread.ID || (i > 0 && (m.ParentID == nil || *m.ParentID != thread.StarterID)) {
				return bad
			}
			if (m.BodyState != "observed" && m.BodyState != "unmapped") || (m.BodyState == "observed" && m.Blocks == nil) ||
				(m.BodyState == "unmapped" && (i == 0 || m.Blocks != nil)) {
				return bad
			}
			for _, value := range []string{m.ID, m.ThreadID} {
				if err := v.string(value, false, false); err != nil {
					return err
				}
				if _, changed := scrubSelectedText(value); changed {
					return bad
				}
			}
			for _, value := range []*string{m.ParentID, m.Author.ID, m.Author.Name, m.Author.NetworkID, m.Author.NativeType, m.CreatedRaw, m.UpdatedRaw} {
				if value != nil {
					if err := v.string(*value, false, true); err != nil {
						return err
					}
					if clean, _ := scrubSelectedText(*value); clean != *value {
						return bad
					}
				}
			}
			for _, pair := range []struct {
				raw *string
				at  *time.Time
			}{{m.CreatedRaw, m.CreatedAt}, {m.UpdatedRaw, m.UpdatedAt}} {
				if err := validateClock(pair.raw, pair.at); err != nil {
					return err
				}
				if pair.at == nil {
					unknownClocks++
				}
			}
			for _, value := range m.Blocks {
				if err := v.string(value, true, true); err != nil {
					return err
				}
				if clean, _ := scrubSelectedText(value); clean != value {
					return bad
				}
			}
			for _, link := range m.Links {
				if err := v.string(link.URL, false, false); err != nil {
					return err
				}
				if !SafeThreadLink(link.URL) {
					return bad
				}
			}
			for _, a := range m.Attachments {
				if err := v.string(a.ID, false, false); err != nil {
					return err
				}
				if _, changed := scrubSelectedText(a.ID); changed {
					return bad
				}
				for _, value := range []*string{a.Title, a.NativeType, a.URL} {
					if value != nil {
						if err := v.string(*value, false, true); err != nil {
							return err
						}
						if clean, _ := scrubSelectedText(*value); clean != *value {
							return bad
						}
					}
				}
				if a.URL != nil && !SafeThreadLink(*a.URL) {
					return bad
				}
			}
		}
	}
	if unknownClocks > clockLoss {
		return bad
	}
	encoded, _ := json.Marshal(r)
	if len(encoded) > 8388608 {
		return &ReadError{Code: "too_large"}
	}
	return ctx.Err()
}

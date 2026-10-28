package engagecontent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func selectedFixture(t *testing.T) (ThreadTarget, scriptResult) {
	t.Helper()
	target, err := ParseThreadURL("https://engage.cloud.microsoft/main/threads/" + selectedTestID())
	if err != nil {
		t.Fatal(err)
	}
	raw := nativeResult()
	raw.Threads[0].ID = target.ThreadID
	name, clock := "Synthetic author", "2026-10-27T12:00:00Z"
	a := SelectedAuthor{ID: &name, Name: &name, NetworkID: &raw.Account.NetworkID}
	m := SelectedMessage{ID: "starter", ThreadID: target.ThreadID, Author: a, CreatedRaw: &clock, UpdatedRaw: &clock,
		Blocks: []string{"starter text"}, BodyState: "observed", Links: []SelectedLink{{URL: "https://example.test/a"}}, Attachments: []SelectedAttachment{{ID: "a", Title: &name}}}
	reply := m
	reply.ID, reply.ParentID, reply.Blocks = "reply", &m.ID, []string{"reply-only-search"}
	raw.Threads[0].Blocks = m.Blocks
	raw.Threads[0].Selected = rawValue(SelectedThread{Messages: []SelectedMessage{m, reply}, Replies: ReplyPage{EdgesObserved: 1}})
	return target, raw
}

func TestSelectedRetainedEvidenceRefusals(t *testing.T) {
	target, raw := selectedFixture(t)
	good, err := decodeSelected(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Result){
		"target-url":        func(r *Result) { r.Selection.URL += "?sig=private" },
		"qualification":     func(r *Result) { r.Qualification = "invented" },
		"duplicate-loss":    func(r *Result) { r.Losses = append(r.Losses, r.Losses[0]) },
		"unsupported-loss":  func(r *Result) { r.Losses = []Loss{{"unknown", 1}} },
		"no-threads":        func(r *Result) { r.Threads = nil },
		"no-selected":       func(r *Result) { r.Threads[0].Selected = nil },
		"no-messages":       func(r *Result) { r.Threads[0].Selected.Messages = nil },
		"body-mismatch":     func(r *Result) { r.Threads[0].Blocks = []string{"changed"} },
		"page-negative":     func(r *Result) { n := -1; r.Threads[0].Selected.Replies.TotalCount = &n },
		"wrong-thread":      func(r *Result) { r.Threads[0].Selected.Messages[0].ThreadID = "other" },
		"missing-parent":    func(r *Result) { r.Threads[0].Selected.Messages[1].ParentID = nil },
		"body-state":        func(r *Result) { r.Threads[0].Selected.Messages[1].BodyState = "invented" },
		"nil-observed-body": func(r *Result) { r.Threads[0].Selected.Messages[1].Blocks = nil },
		"unmapped-nonempty": func(r *Result) { r.Threads[0].Selected.Messages[1].BodyState = "unmapped" },
		"message-id":        func(r *Result) { r.Threads[0].Selected.Messages[1].ID = "" },
		"secret-id":         func(r *Result) { r.Threads[0].Selected.Messages[1].ID = "https://example.test/a?sig=private" },
		"secret-author": func(r *Result) {
			s := "https://example.test/a?sig=private"
			r.Threads[0].Selected.Messages[1].Author.Name = &s
		},
		"secret-source-account": func(r *Result) { r.Account.UserID = "https://example.test/a?sig=private" },
		"secret-root-title":     func(r *Result) { value := "https://example.test/a?sig=private"; r.Threads[0].Title = &value },
		"secret-root-language":  func(r *Result) { value := "https://example.test/a?sig=private"; r.Threads[0].Language = &value },
		"secret-body": func(r *Result) {
			r.Threads[0].Selected.Messages[1].Blocks = []string{"https://example.test/a?sig=private"}
		},
		"unsafe-link":      func(r *Result) { r.Threads[0].Selected.Messages[1].Links = []SelectedLink{{"javascript:private"}} },
		"empty-link":       func(r *Result) { r.Threads[0].Selected.Messages[1].Links = []SelectedLink{{""}} },
		"empty-attachment": func(r *Result) { r.Threads[0].Selected.Messages[1].Attachments = []SelectedAttachment{{}} },
		"secret-attachment-id": func(r *Result) {
			r.Threads[0].Selected.Messages[1].Attachments = []SelectedAttachment{{ID: "https://example.test/a?sig=private"}}
		},
		"secret-attachment-title": func(r *Result) {
			s := "https://example.test/a?sig=private"
			r.Threads[0].Selected.Messages[1].Attachments = []SelectedAttachment{{ID: "a", Title: &s}}
		},
		"unsafe-attachment-url": func(r *Result) {
			s := "javascript:private"
			r.Threads[0].Selected.Messages[1].Attachments = []SelectedAttachment{{ID: "a", URL: &s}}
		},
		"wrong-clock": func(r *Result) { at := time.Now(); r.Threads[0].Selected.Messages[1].CreatedAt = &at },
		"missing-clock-loss": func(r *Result) {
			r.Threads[0].Selected.Messages[1].CreatedRaw = nil
			r.Threads[0].Selected.Messages[1].CreatedAt = nil
		},
		"missing-clock-loss-without-attachments": func(r *Result) {
			for i := range r.Threads[0].Selected.Messages {
				r.Threads[0].Selected.Messages[i].Attachments = nil
			}
			r.Threads[0].Selected.Messages[1].CreatedRaw = nil
			r.Threads[0].Selected.Messages[1].CreatedAt = nil
		},
		"too-many-links":       func(r *Result) { r.Threads[0].Selected.Messages[1].Links = make([]SelectedLink, 129) },
		"too-many-attachments": func(r *Result) { r.Threads[0].Selected.Messages[1].Attachments = make([]SelectedAttachment, 129) },
		"too-many-blocks":      func(r *Result) { r.Threads[0].Selected.Messages[1].Blocks = make([]string, 4097) },
		"invalid-string":       func(r *Result) { r.Threads[0].Selected.Messages[1].ID = "\x00" },
	} {
		t.Run(name, func(t *testing.T) {
			var r Result
			b, _ := json.Marshal(good)
			_ = json.Unmarshal(b, &r)
			mutate(&r)
			if ValidateResult(context.Background(), r) == nil {
				t.Fatal("corrupt retained evidence admitted")
			}
		})
	}
}
func TestSelectedEmptyObservedBodyAndAttachmentIDRedaction(t *testing.T) {
	target, raw := selectedFixture(t)
	var s SelectedThread
	_ = json.Unmarshal(raw.Threads[0].Selected, &s)
	s.Messages[0].Blocks = []string{}
	raw.Threads[0].Blocks = []string{}
	s.Messages[0].Attachments[0].ID = "https://example.test/a?sig=private"
	raw.Threads[0].Selected = rawValue(s)
	r, err := decodeSelected(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Threads[0].Selected.Messages[0].Attachments) != 0 {
		t.Fatal("unsafe attachment identity retained")
	}
}

func TestSelectedModelBindingSafetyAndOwnership(t *testing.T) {
	target, raw := selectedFixture(t)
	got, err := decodeSelected(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selection == nil || got.Qualification != SelectedQualification || got.Threads[0].Selected.Messages[1].CreatedAt == nil || got.Complete {
		t.Fatal("selected evidence missing")
	}
	if err := ValidateResult(context.Background(), got); err != nil {
		t.Fatal("retained result refused", err)
	}
	messages := got.Threads[0].Selected.Messages
	messages[1].Blocks[0] = "changed"
	if strings.Contains(string(raw.Threads[0].Selected), "changed") {
		t.Fatal("aliased native input")
	}
	bad := raw
	bad.Threads = append([]scriptThread{}, raw.Threads...)
	bad.Threads[0].ID = "another"
	if _, err := decodeSelected(context.Background(), target, bad); err == nil {
		t.Fatal("wrong target admitted")
	}
	target.GroupID = "another"
	if _, err := decodeSelected(context.Background(), target, raw); err == nil {
		t.Fatal("wrong community admitted")
	}
	target.GroupID = ""
	var selected SelectedThread
	_ = json.Unmarshal(raw.Threads[0].Selected, &selected)
	selected.Messages[0].Blocks[0] = "unsafe https://example.test/a?tempauth=private"
	selected.Messages[0].Links[0].URL = "https://example.test/a?sig=private"
	raw.Threads[0].Blocks = selected.Messages[0].Blocks
	raw.Threads[0].Selected = rawValue(selected)
	got, err = decodeSelected(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "private") || len(got.Threads[0].Selected.Messages[0].Links) != 0 {
		t.Fatal("unsafe data retained")
	}
}

func TestSelectedModelRefusesAbsentAndForeignReply(t *testing.T) {
	target, raw := selectedFixture(t)
	for _, mutate := range []func(*scriptResult){
		func(r *scriptResult) {
			r.Threads = nil
			r.State = "metadata_only"
			r.Losses = []Loss{{"native_content_not_observed", 1}}
		},
		func(r *scriptResult) { r.Threads[0].Selected = nil },
		func(r *scriptResult) { r.Threads[0].Selected = rawValue(SelectedThread{}) },
		func(r *scriptResult) {
			var s SelectedThread
			_ = json.Unmarshal(r.Threads[0].Selected, &s)
			s.Messages[1].ThreadID = "foreign"
			r.Threads[0].Selected = rawValue(s)
		},
		func(r *scriptResult) {
			var s SelectedThread
			_ = json.Unmarshal(r.Threads[0].Selected, &s)
			s.Messages[1].ParentID = nil
			r.Threads[0].Selected = rawValue(s)
		},
	} {
		copy := raw
		copy.Threads = append([]scriptThread{}, raw.Threads...)
		mutate(&copy)
		got, err := decodeSelected(context.Background(), target, copy)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatal("unverified selected evidence admitted")
		}
	}
}

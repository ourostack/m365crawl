package engagecontent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSelectedWholeBodyURLRedaction(t *testing.T) {
	target, raw := selectedFixture(t)
	var selected SelectedThread
	if err := json.Unmarshal(raw.Threads[0].Selected, &selected); err != nil {
		t.Fatal(err)
	}
	unsafe := "https://example.test/it's-a-file?tempauth=synthetic-private"
	if SafeThreadLink(unsafe) {
		t.Fatal("whole URL unexpectedly considered safe")
	}
	selected.Messages[0].Blocks = []string{"before " + unsafe + " after"}
	raw.Threads[0].Blocks = selected.Messages[0].Blocks
	raw.Threads[0].Selected = rawValue(selected)
	got, err := decodeSelected(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-private") {
		t.Fatalf("unsafe body URL retained after decode and ValidateResult: %s", got.Threads[0].Selected.Messages[0].Blocks[0])
	}
}

func TestSelectedWholeURIBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, uri string
		safe      bool
	}{
		{"apostrophe", "https://example.test/it's-a-file?view=1", true},
		{"encoded-apostrophe", "https://example.test/it%27s-a-file?view=1", true},
		{"subdelimiters", "https://example.test/!$&'()*+,;=:@-._~", true},
		{"prose", "it's safe text", true},
		{"tempauth", "https://example.test/it's-a-file?tempauth=synthetic-private", false},
		{"sig", "https://example.test/it's-a-file?sig=synthetic-private", false},
		{"userinfo", "https://user:synthetic-private@example.test/it's-a-file", false},
		{"fragment", "https://example.test/it's-a-file#sig=synthetic-private", false},
		{"protocol-relative", "//example.test/it's-a-file?tempauth=synthetic-private", false},
		{"subdelimiters-unsafe", "https://example.test/!$&'()*+,;=:@-._~?tempauth=synthetic-private", false},
		{"encoded-apostrophe-unsafe", "https://example.test/it%27s-a-file?tempauth=synthetic-private", false},
		{"encoded-key", "https://example.test/it's-a-file?%74empauth=synthetic-private", false},
		{"encoded-delimiters", "https://example.test/it%22s%3Ca%3E?tempauth=synthetic-private", false},
		{"download", "https://example.test/it's-a-file?downloadUrl=synthetic-private", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := scrubSelectedText("before " + tc.uri + " after")
			want := "before [redacted] after"
			if tc.safe {
				want = "before " + tc.uri + " after"
			}
			if got != want || changed == tc.safe {
				t.Fatalf("whole-value handling: got %q changed=%t want %q", got, changed, want)
			}
			if !tc.safe && SafeThreadLink(tc.uri) {
				t.Fatal("complete unsafe value admitted")
			}
		})
	}
}

func TestSelectedDescriptiveURLRedaction(t *testing.T) {
	target, raw := selectedFixture(t)
	var selected SelectedThread
	if err := json.Unmarshal(raw.Threads[0].Selected, &selected); err != nil {
		t.Fatal(err)
	}
	unsafe := "https://example.test/it's-a-file?tempauth=synthetic-private"
	safe := "https://example.test/it's-safe?view=1"
	selected.Messages[0].Author.Name = &unsafe
	selected.Messages[0].Attachments[0].Title = &unsafe
	selected.Messages[0].Attachments[0].NativeType = &unsafe
	selected.Messages[0].Links = []SelectedLink{{URL: unsafe}, {URL: safe}}
	selected.Messages[0].Attachments[0].URL = &unsafe
	selected.Messages[1].Blocks = []string{"before " + unsafe + " after", safe}
	raw.Threads[0].Selected = rawValue(selected)
	raw.Threads[0].Title, raw.Threads[0].Language = rawValue(unsafe), rawValue(unsafe)
	got, err := decodeSelected(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	s := got.Threads[0].Selected
	if *got.Threads[0].Title != "[redacted]" || *got.Threads[0].Language != "[redacted]" ||
		*s.Messages[0].Author.Name != "[redacted]" || *s.Messages[0].Attachments[0].Title != "[redacted]" ||
		*s.Messages[0].Attachments[0].NativeType != "[redacted]" || s.Messages[0].Attachments[0].URL != nil ||
		len(s.Messages[0].Links) != 1 || s.Messages[0].Links[0].URL != safe ||
		s.Messages[1].Blocks[0] != "before [redacted] after" || s.Messages[1].Blocks[1] != safe {
		t.Fatal("body/descriptive values were not sanitized whole or safe value was lost")
	}
	if err := ValidateResult(context.Background(), got); err != nil {
		t.Fatal("sanitized evidence must remain admissible", err)
	}
}

func TestSelectedRetainedWholeURIRefusal(t *testing.T) {
	unsafe := "https://example.test/it's-a-file?tempauth=synthetic-private"
	for name, mutate := range map[string]func(*Result){
		"body": func(r *Result) {
			r.Threads[0].Blocks = []string{unsafe}
			r.Threads[0].Selected.Messages[0].Blocks = r.Threads[0].Blocks
		},
		"reply":      func(r *Result) { r.Threads[0].Selected.Messages[1].Blocks = []string{unsafe} },
		"title":      func(r *Result) { r.Threads[0].Title = &unsafe },
		"author":     func(r *Result) { r.Threads[0].Selected.Messages[0].Author.Name = &unsafe },
		"attachment": func(r *Result) { r.Threads[0].Selected.Messages[0].Attachments[0].Title = &unsafe },
	} {
		t.Run(name, func(t *testing.T) {
			target, raw := selectedFixture(t)
			got, err := decodeSelected(context.Background(), target, raw)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&got)
			if ValidateResult(context.Background(), got) == nil {
				t.Fatal("unsafe retained value accepted")
			}
		})
	}
}

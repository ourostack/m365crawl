//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookowacal"
)

func TestOWACalendarIndependentHTMLLiterals(t *testing.T) {
	for _, tc := range []struct{ source, plain string }{
		{"", ""},
		{"<p>one &amp; two&nbsp; words</p><div>next</div>", "one & two words\nnext"},
		{"<b>adjacent</b><i>inline</i>", "adjacentinline"},
		{"a<br>b<br><br>c", "a\nb\n\nc"},
		{"before<script>private ignored()</script><style>ignored{}</style>after", "beforeafter"},
		{"<!-- hidden --><p>visible</p>", "visible"},
		{`<emoji alt="&lt;3"></emoji><img itemtype="EMOJI" alt="😀"><img alt="not included">`, "<3😀"},
		{"<table><tr><td>one</td><td>two</td></tr><tr><td>three</td></tr></table>", "onetwo\nthree"},
		{"1 < 2 &amp; 3 > 2", "1 < 2 & 3 > 2"},
	} {
		plain, err := owaCalendarIndependentHTML(t.Context(), tc.source)
		if err != nil || plain != tc.plain {
			t.Fatal("independent_calendar_html_literal_mismatch")
		}
		value := owaObject("UID", "AA11", "Start", "2026-10-20T00:00:00Z", "End", "2026-10-21T00:00:00Z",
			"LastModifiedTime", "2026-10-19T00:00:00Z", "Body", owaObject("BodyType", "HTML", "Value", tc.source))
		actual, notes, err := outlookowacal.MapEvent("outlook/Synthetic", value)
		if err != nil || actual.BodyText != tc.plain || actual.BodyHTML != tc.source || notes.Unmapped != 0 {
			t.Fatal("calendar_html_literal_actual_differential_mismatch")
		}
	}
	for _, source := range []string{"<p", "<!-- truncated", "<svg>foreign</svg>", "<textarea>raw &amp; text</textarea>", string([]byte{0xff}), "zero\x00byte"} {
		if _, err := owaCalendarIndependentHTML(t.Context(), source); err == nil {
			t.Fatal("unproved_html_accepted_as_expected_text")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owaCalendarIndependentHTML(ctx, "Synthetic"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled_html_projection_accepted")
	}
}

func TestOWACalendarBodyOracleLiteralDifferential(t *testing.T) {
	for _, tc := range []struct {
		body              any
		html, text, kind  string
		known             bool
		invalid, redacted int
	}{
		{nil, "", "", "", false, 0, 0},
		{owaObject(), "", "", "", false, 0, 0},
		{false, "", "", "", false, 1, 0},
		{owaObject("BodyType", "HTML"), "", "", "", false, 1, 0},
		{owaObject("BodyType", 12.0, "Value", "Synthetic"), "", "", "", false, 1, 0},
		{owaObject("BodyType", "HTML", "Value", "<p>first</p><p>second</p>", "IsTruncated", false), "<p>first</p><p>second</p>", "first\nsecond", "html", true, 0, 0},
		{owaObject("BodyType", "Text", "Value", " exact \n text "), "", " exact \n text ", "text", true, 0, 0},
		{owaObject("BodyType", "HTML", "Value", ""), "", "", "html", true, 0, 0},
		{owaObject("BodyType", "HTML", "Value", "available", "IsTruncated", true), "", "", "", false, 1, 0},
		{owaObject("BodyType", "HTML", "Value", "available", "IsTruncated", "false"), "", "", "", false, 1, 0},
		{owaObject("BodyType", "Synthetic Unsupported", "Value", "available"), "", "", "", false, 1, 0},
		{owaObject("BodyType", "Text", "Value", "Bearer SYNTHETIC"), "", "[redacted]", "text", true, 0, 1},
	} {
		value := owaObject("UID", "AA11", "Start", "2026-10-20T00:00:00Z", "End", "2026-10-21T00:00:00Z",
			"LastModifiedTime", "2026-10-19T00:00:00Z", "Body", tc.body)
		expected, err := owaProjectBody(t.Context(), value)
		if err != nil || expected.HTML != tc.html || expected.Text != tc.text || expected.Kind != tc.kind ||
			expected.Known != tc.known || expected.Invalid != tc.invalid || expected.Redacted != tc.redacted {
			t.Fatal("independent_cached_body_evidence_mismatch")
		}
		actual, notes, err := outlookowacal.MapEvent("outlook/Synthetic", value)
		unknown := false
		for _, field := range actual.Unknown {
			unknown = unknown || field == calendar.FieldBody
		}
		if err != nil || actual.BodyHTML != tc.html || actual.BodyText != tc.text || actual.BodyType != tc.kind ||
			unknown == tc.known || notes.Unmapped != tc.invalid || notes.Redacted != tc.redacted {
			t.Fatal("cached_body_value_knowledge_differential_mismatch")
		}
	}
}

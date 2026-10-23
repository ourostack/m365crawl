package outlookowacal

import (
	"strings"
	"testing"
)

func TestMapEventSignedQueryPrivacy(t *testing.T) {
	for _, field := range []string{"Subject", "Location", "OnlineMeetingJoinUrl", "Body"} {
		t.Run(field, func(t *testing.T) {
			value := event()
			source := "https://example.invalid/?x=1&sig=SYNTHETIC&end=2"
			if field == "Body" {
				replace(value, "Body", object("BodyType", "HTML", "Value", `<p><a href="`+source+`">Synthetic</a></p>`, "IsTruncated", false))
			} else {
				replace(value, field, source)
			}
			got, notes, err := MapEvent("outlook/Synthetic", value)
			if err != nil || notes.Redacted != 1 {
				t.Fatal("signed_query_scrub_not_counted")
			}
			output := map[string]string{"Subject": got.Subject, "Location": got.Location, "OnlineMeetingJoinUrl": got.OnlineMeetingURL, "Body": got.BodyHTML}[field]
			if strings.Contains(output, "sig=SYNTHETIC") || !strings.Contains(output, "sig=[redacted]&end=2") {
				t.Fatal("signed_query_value_survived_or_delimiters_changed")
			}
		})
	}
}

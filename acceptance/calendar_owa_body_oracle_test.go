//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"strings"
)

type owaBodyExpected struct {
	HTML, Text, Kind  string
	Known             bool
	Invalid, Redacted int
}

func owaProjectBody(ctx context.Context, value any) (owaBodyExpected, error) {
	var out owaBodyExpected
	if err := ctx.Err(); err != nil {
		return out, err
	}
	fields, valid := owaSurveyObject(value)
	if !valid {
		return out, errors.New("calendar_oracle_object_refused")
	}
	raw := fields["Body"]
	if !owaSurveyPresent(raw) {
		return out, nil
	}
	body, valid := owaSurveyObject(raw)
	if !valid {
		out.Invalid = 1
		return out, nil
	}
	haveKind, haveValue := owaSurveyPresent(body["BodyType"]), owaSurveyPresent(body["Value"])
	if !haveKind || !haveValue {
		if haveKind || haveValue {
			out.Invalid = 1
		}
		return out, nil
	}
	privacy := owaOptionalExpected{}
	kind, content := privacy.text(ctx, body["BodyType"]), privacy.text(ctx, body["Value"])
	out.Invalid, out.Redacted = privacy.Invalid, privacy.Redacted
	if privacy.err != nil || !kind.Known || !content.Known {
		return out, privacy.err
	}
	if truncated := body["IsTruncated"]; owaSurveyPresent(truncated) {
		flag, valid := truncated.(bool)
		if !valid || flag {
			out.Invalid++
			return out, nil
		}
	}
	switch strings.ToLower(kind.Value) {
	case "text":
		out.Text, out.Kind, out.Known = content.Value, "text", true
	case "html":
		plain, err := owaCalendarIndependentHTML(ctx, content.Value)
		if err != nil {
			return out, err
		}
		out.HTML, out.Text, out.Kind, out.Known = content.Value, plain, "html", true
	default:
		out.Invalid++
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}

package teamsimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/ourostack/m365crawl/internal/errs"
)

const (
	MaxHTMLBytes  = 1048576
	maxTokenBytes = 65536
	maxImages     = 4096
	maxAttributes = 64
	maxIDBytes    = 1024
	maxURLBytes   = 8192
)

type InspectionState string

const (
	InspectionComplete InspectionState = "complete"
	InspectionPartial  InspectionState = "partial"
	InspectionUnknown  InspectionState = "unknown"
)

type Reference struct {
	ID              string `json:"image_id"`
	OccurrenceCount int    `json:"occurrence_count"`
	Width           *int   `json:"width"`
	Height          *int   `json:"height"`
	Selection       string `json:"selection"`
	Code            string `json:"code,omitempty"`
	source          string
	binding         string
}

func (r Reference) String() string   { return "image reference: " + r.Selection }
func (r Reference) GoString() string { return r.String() }
func (r Reference) Source() string   { return r.source }
func (r Reference) Binding() string  { return r.binding }

type Inspection struct {
	State                     InspectionState
	References                []Reference
	DecorativeEmojiCount      int
	UnsupportedReferenceCount int
	ReasonCodes               []string
}

func ValidComponent(value string) bool {
	if value == "" || len(value) > maxIDBytes || !utf8.ValidString(value) ||
		value == "." || value == ".." || strings.ContainsAny(value, "|/\\") {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func InspectHTML(ctx context.Context, body string) (Inspection, error) {
	result := Inspection{State: InspectionUnknown, References: []Reference{}, ReasonCodes: []string{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	switch {
	case len(body) > MaxHTMLBytes:
		result.ReasonCodes = append(result.ReasonCodes, "image_body_too_large")
		return result, nil
	case strings.TrimSpace(body) == "":
		result.ReasonCodes = append(result.ReasonCodes, "image_body_unavailable")
		return result, nil
	case !utf8.ValidString(body) || strings.ContainsRune(body, 0):
		result.ReasonCodes = append(result.ReasonCodes, "image_html_invalid")
		return result, nil
	}
	result.State = InspectionComplete
	tokenizer := html.NewTokenizer(strings.NewReader(body))
	tokenizer.SetMaxBuf(maxTokenBytes + 1)
	indices := map[string]int{}
	imageCount := 0
	incomplete := func(code string) {
		result.State = InspectionPartial
		result.ReasonCodes = append(result.ReasonCodes, code)
		for i := range result.References {
			if result.References[i].Selection == "supported" {
				result.References[i].Selection = "inspection_incomplete"
				result.References[i].Code = "image_inspection_incomplete"
			}
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			incomplete("interrupted")
			return result, err
		}
		kind := tokenizer.Next()
		if len(tokenizer.Raw()) > maxTokenBytes {
			incomplete("image_token_limit")
			return result, nil
		}
		if kind == html.ErrorToken {
			if errors.Is(tokenizer.Err(), io.EOF) {
				if len(tokenizer.Raw()) != 0 {
					incomplete("image_html_invalid")
				}
				return result, nil
			}
			incomplete("image_token_limit")
			return result, nil
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		attributeCount, duplicate := rawImageAttributes(tokenizer.Raw())
		token := tokenizer.Token()
		if token.Data != "img" {
			continue
		}
		imageCount++
		if imageCount > maxImages {
			incomplete("image_element_limit")
			return result, nil
		}
		if attributeCount > maxAttributes {
			incomplete("image_attribute_limit")
			return result, nil
		}
		ref, decorative := inspectImage(token.Attr)
		if duplicate {
			decorative = false
			ref.Selection, ref.Code = "ambiguous", "image_reference_ambiguous"
			ref.OccurrenceCount = 1
		}
		if decorative {
			result.DecorativeEmojiCount++
			continue
		}
		if ref.Selection != "supported" {
			result.UnsupportedReferenceCount++
		}
		if index, exists := indices[ref.ID]; exists && ref.ID != "" {
			prior := &result.References[index]
			if prior.binding != ref.binding || prior.Selection != ref.Selection {
				if prior.Selection == "supported" {
					result.UnsupportedReferenceCount += prior.OccurrenceCount
				}
				if ref.Selection == "supported" {
					result.UnsupportedReferenceCount++
				}
				prior.Selection, prior.Code = "ambiguous", "image_reference_ambiguous"
			}
			prior.OccurrenceCount++
			continue
		}
		indices[ref.ID] = len(result.References)
		result.References = append(result.References, ref)
	}
}

// The tokenizer retains only the first attribute with each name. Inspect raw
// declarations before normalization so duplicates cannot hide conflicts or bounds.
func rawImageAttributes(raw []byte) (count int, duplicate bool) {
	pos := 1
	for pos < len(raw) && !imageHTMLSpace(raw[pos]) && raw[pos] != '/' && raw[pos] != '>' {
		pos++
	}
	if pos > len(raw) || len(raw) < 2 || !strings.EqualFold(string(raw[1:pos]), "img") {
		return 0, false
	}
	names := map[string]bool{}
	for pos < len(raw) {
		if imageHTMLSpace(raw[pos]) || raw[pos] == '/' {
			pos++
			continue
		}
		if raw[pos] == '>' {
			break
		}
		start := pos
		if raw[pos] == '=' {
			pos++
		}
		for pos < len(raw) && !imageHTMLSpace(raw[pos]) && raw[pos] != '=' && raw[pos] != '/' && raw[pos] != '>' {
			pos++
		}
		count++
		if count > maxAttributes {
			return count, duplicate
		}
		name := strings.ToLower(string(raw[start:pos]))
		duplicate = duplicate || names[name]
		names[name] = true
		for pos < len(raw) && imageHTMLSpace(raw[pos]) {
			pos++
		}
		if pos >= len(raw) || raw[pos] != '=' {
			continue
		}
		pos++
		for pos < len(raw) && imageHTMLSpace(raw[pos]) {
			pos++
		}
		if pos < len(raw) && (raw[pos] == '"' || raw[pos] == '\'') {
			quote := raw[pos]
			pos++
			for pos < len(raw) && raw[pos] != quote {
				pos++
			}
			if pos < len(raw) {
				pos++
			}
		} else {
			for pos < len(raw) && !imageHTMLSpace(raw[pos]) && raw[pos] != '>' {
				pos++
			}
		}
	}
	return count, duplicate
}

func imageHTMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func inspectImage(attributes []html.Attribute) (Reference, bool) {
	values := map[string]string{}
	for _, attr := range attributes {
		values[attr.Key] = attr.Val
	}
	id, source, itemType := values["itemid"], values["src"], values["itemtype"]
	emoji := strings.EqualFold(itemType, "http://schema.skype.com/Emoji")
	for _, class := range strings.Fields(values["class"]) {
		emoji = emoji || strings.EqualFold(class, "Emoji")
	}
	if id == "" && emoji &&
		(itemType == "" || strings.EqualFold(itemType, "http://schema.skype.com/Emoji")) {
		return Reference{}, true
	}
	ref := Reference{
		ID: id, OccurrenceCount: 1, Width: imageDimension(values["width"]),
		Height: imageDimension(values["height"]), Selection: "supported", source: source,
	}
	binding := sha256.Sum256([]byte(strings.Join([]string{id, source, itemType, values["width"], values["height"]}, "\x00")))
	ref.binding = hex.EncodeToString(binding[:])
	if !ValidComponent(id) || len(source) > maxURLBytes || source == "" ||
		strings.ContainsFunc(source, unicode.IsControl) || strings.Contains(source, "[redacted]") ||
		(!strings.HasPrefix(source, "https://") && !strings.HasPrefix(source, "/")) || strings.HasPrefix(source, "//") ||
		(itemType != "" && !strings.EqualFold(itemType, "http://schema.skype.com/AMSImage")) {
		ref.Selection, ref.Code = "unsupported", "image_reference_unsupported"
	}
	if !ValidComponent(id) {
		ref.ID = ""
	}
	if len(source) > maxURLBytes {
		ref.source, ref.binding = "", ""
	}
	return ref, false
}

func imageDimension(value string) *int {
	number, err := strconv.Atoi(value)
	if err != nil || number < 0 {
		return nil
	}
	return &number
}

func Select(inspection Inspection, id string) (Reference, error) {
	if inspection.State != InspectionComplete {
		return Reference{}, imageRefError("image_inspection_incomplete")
	}
	if !ValidComponent(id) {
		return Reference{}, imageRefError("image_reference_unsupported")
	}
	for _, reference := range inspection.References {
		if reference.ID == id {
			if reference.Selection != "supported" {
				return Reference{}, imageRefError(reference.Code)
			}
			return reference, nil
		}
	}
	return Reference{}, imageRefError("image_reference_unsupported")
}

func imageRefError(code string) error {
	if code == "" {
		code = "image_reference_unsupported"
	}
	return &errs.Coded{Code: code, Exit: errs.ExitEnvironment,
		Message: "the selected cached image reference is not uniquely supported",
		Fix:     "Inspect the exact message with content images; incomplete references cannot authorize fetching."}
}

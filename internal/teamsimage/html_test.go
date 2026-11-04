package teamsimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

const generatedImage = `<img itemtype="http://schema.skype.com/AMSImage" itemid="synthetic-image" src="https://example.test/image" width="7" height="3">`

func TestImageInventoryMalformedEOF(t *testing.T) {
	inspection, err := InspectHTML(context.Background(), generatedImage+`<img itemid="unfinished`)
	if err != nil || inspection.State != InspectionPartial || len(inspection.ReasonCodes) == 0 {
		t.Fatalf("unterminated image markup claimed complete: %v %v", inspection, err)
	}
	if _, err := Select(inspection, "synthetic-image"); err == nil {
		t.Fatal("malformed HTML prefix authorized selection")
	}
}

func TestImageInventoryDuplicateAttributeBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		repeats int
		want    InspectionState
	}{
		{"exact", 62, InspectionComplete},
		{"over", 63, InspectionPartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<img itemid="synthetic-image" src="https://example.test/image"` +
				strings.Repeat(` class="Emoji"`, tc.repeats) + `>`
			inspection, err := InspectHTML(context.Background(), body)
			if err != nil || inspection.State != tc.want {
				t.Fatalf("raw duplicate attribute bound bypassed: %v %v", inspection, err)
			}
		})
	}
}

func TestImageInventoryRawAttributeSyntax(t *testing.T) {
	for _, body := range []string{
		`<IMG ITEMID='synthetic-image' SRC='https://example.test/image'>`,
		`<img itemid=synthetic-image src=https://example.test/image disabled>`,
		"<img\titemid = \"synthetic-image\"\r\nsrc = \"https://example.test/image\"\f/>",
		`<img itemid="synthetic-image" alt='itemid="another" > src="hidden"' src="https://example.test/image">`,
	} {
		inspection, err := InspectHTML(context.Background(), body)
		if err != nil || inspection.State != InspectionComplete || len(inspection.References) != 1 ||
			inspection.References[0].Selection != "supported" {
			t.Fatalf("quoted, unquoted or boolean attributes changed selection: %v %v", inspection, err)
		}
	}
	for _, body := range []string{
		`<img ITEMID=synthetic-image itemid=another src=https://example.test/image>`,
		`<img class=Emoji class=Emoji>`,
	} {
		inspection, err := InspectHTML(context.Background(), body)
		if err != nil || inspection.DecorativeEmojiCount != 0 || len(inspection.References) != 1 ||
			inspection.References[0].Selection != "ambiguous" {
			t.Fatalf("duplicate declarations lost their refusal: %v %v", inspection, err)
		}
	}
	for _, tc := range []struct {
		raw   string
		count int
	}{
		{"", 0}, {"<", 0}, {"<p>", 0}, {"<img flag", 1},
		{"<img flag=", 1}, {"<img alt='unfinished", 1}, {"<img =odd=x>", 1},
	} {
		count, duplicate := rawImageAttributes([]byte(tc.raw))
		if count != tc.count || duplicate {
			t.Fatalf("bounded raw declaration accounting: %d %v", count, duplicate)
		}
	}
}

func TestImageInventoryOpaqueBindingAndDecoration(t *testing.T) {
	inspection, err := InspectHTML(context.Background(), generatedImage+generatedImage+`<img class="Emoji">`)
	if err != nil || inspection.State != InspectionComplete || len(inspection.References) != 1 ||
		inspection.DecorativeEmojiCount != 1 || inspection.References[0].OccurrenceCount != 2 {
		t.Fatalf("wrong complete inventory: %v %v", inspection, err)
	}
	ref, err := Select(inspection, "synthetic-image")
	if err != nil || ref.Width == nil || *ref.Width != 7 || ref.Height == nil || *ref.Height != 3 ||
		ref.Source() != "https://example.test/image" || len(ref.Binding()) != 64 {
		t.Fatalf("wrong selected binding: %v %v", ref, err)
	}
	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw)+fmt.Sprintf("%v %#v", ref, ref)+ref.Binding(), "example.test") {
		t.Fatal("private source leaked through encoding, formatting or binding fingerprint")
	}
}

func TestImageInventoryUnknownAndCancellation(t *testing.T) {
	for _, body := range []string{"", " \t\n", string([]byte{255}), "<p>\x00</p>"} {
		inspection, err := InspectHTML(context.Background(), body)
		if err != nil || inspection.State != InspectionUnknown || len(inspection.ReasonCodes) == 0 {
			t.Fatalf("unknown became complete: %v %v", inspection, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := InspectHTML(ctx, generatedImage)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
}

func TestImageSelectionAmbiguityAndUnsupported(t *testing.T) {
	for index, body := range []string{
		generatedImage + strings.Replace(generatedImage, `width="7"`, `width="8"`, 1),
		generatedImage + strings.Replace(generatedImage, "example.test/image", "example.test/other", 1),
		strings.Replace(generatedImage, `itemid="synthetic-image"`, `itemid="synthetic-image" itemid="conflicting"`, 1),
		strings.Replace(generatedImage, `src="https://example.test/image"`, `src="[redacted]"`, 1),
		strings.Replace(generatedImage, "https://example.test/image", "//example.test/image", 1),
		strings.Replace(generatedImage, "AMSImage", "Emoji", 1),
	} {
		inspection, err := InspectHTML(context.Background(), body)
		if err != nil || inspection.State != InspectionComplete || inspection.UnsupportedReferenceCount == 0 {
			t.Fatalf("unsupported case %d was hidden: %v %v", index, inspection, err)
		}
		_, err = Select(inspection, "synthetic-image")
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Exit != errs.ExitEnvironment || coded.Code == "" {
			t.Fatalf("unsupported selection admitted: %v", err)
		}
	}
	inspection, err := InspectHTML(context.Background(), generatedImage)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"absent", "bad|id", ""} {
		if _, err := Select(inspection, id); err == nil {
			t.Fatal("unknown or invalid selection admitted")
		}
	}
	if _, err := Select(Inspection{State: InspectionPartial}, "synthetic-image"); err == nil {
		t.Fatal("partial prefix admitted")
	}
}

func TestImageInventoryBoundedComponentsAndDimensions(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", "a\\b", "a|b", "a\nb", strings.Repeat("x", 1025), string([]byte{255})} {
		if ValidComponent(bad) {
			t.Fatal("unsafe component admitted")
		}
	}

	if !ValidComponent(strings.Repeat("x", 1024)) {
		t.Fatal("exact component bound refused")
	}
	body := strings.Replace(generatedImage, `width="7" height="3"`, `width="7px" height="-1"`, 1)
	inspection, err := InspectHTML(context.Background(), body)
	if err != nil || len(inspection.References) != 1 || inspection.References[0].Width != nil ||
		inspection.References[0].Height != nil {
		t.Fatalf("unavailable presentation dimensions became pixels: %v %v", inspection, err)
	}
}

func TestImageInventoryLibraryLimitsAndRefusals(t *testing.T) {
	for _, tc := range []struct {
		body string
		want InspectionState
	}{
		{strings.Repeat("<p>x</p>", 131072), InspectionComplete},
		{strings.Repeat("x", MaxHTMLBytes+1), InspectionUnknown},
		{strings.Repeat("x", maxTokenBytes+1), InspectionPartial},
		{"<!--" + strings.Repeat("x", maxTokenBytes+1) + "-->", InspectionPartial},
		{`<img alt="` + strings.Repeat("x", maxTokenBytes+1) + `">`, InspectionPartial},
		{strings.Repeat(generatedImage, maxImages+1), InspectionPartial},
	} {
		inspection, err := InspectHTML(context.Background(), tc.body)
		if err != nil || inspection.State != tc.want {
			t.Fatalf("library bound: state=%s want=%s err=%v", inspection.State, tc.want, err)
		}
	}
	for _, body := range []string{
		strings.Replace(generatedImage, "synthetic-image", "bad|id", 1),
		strings.Replace(generatedImage, "https://example.test/image", "https://"+strings.Repeat("x", maxURLBytes), 1),
	} {
		inspection, err := InspectHTML(context.Background(), body)
		if err != nil || len(inspection.References) != 1 || inspection.References[0].Selection != "unsupported" {
			t.Fatalf("unsupported library reference admitted: %v %v", inspection, err)
		}
	}
	inspection := Inspection{State: InspectionComplete, References: []Reference{{ID: "synthetic-image", Selection: "unsupported"}}}
	if _, err := Select(inspection, "synthetic-image"); err == nil {
		t.Fatal("missing refusal code admitted a reference")
	}
}

type imageCancelAfterChecks struct {
	context.Context
	cancel context.CancelFunc
	left   atomic.Int32
}

func (c *imageCancelAfterChecks) Err() error {
	if c.left.Add(-1) == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestImageInventoryCancellationAfterPrefix(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &imageCancelAfterChecks{Context: base, cancel: cancel}
	ctx.left.Store(3)
	inspection, err := InspectHTML(ctx, generatedImage+"<p>more</p>")
	if !errors.Is(err, context.Canceled) || inspection.State != InspectionPartial ||
		len(inspection.References) != 1 || inspection.References[0].Selection != "inspection_incomplete" {
		t.Fatalf("cancelled prefix remained selectable: %v %v", inspection, err)
	}
}

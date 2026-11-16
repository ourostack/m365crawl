package cli

import (
	"fmt"
	"strings"

	"github.com/openclaw/crawlkit/output"
	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/errs"
)

// missingWhyFix is the next step when a write names no reason: the agent must say who asked.
const missingWhyFix = "Give --why: who asked for this, and what they said, then ask again."

// previewTarget is the chat or mail a write acts on, by its id.
type previewTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// writePreview is the outcome/1 a --dry-run write prints: planned, who would see it, nothing sent or changed.
type writePreview struct {
	Ax            string          `json:"ax"`
	Action        string          `json:"action"`
	Effect        string          `json:"effect"`
	Audience      previewAudience `json:"audience"`
	Target        *previewTarget  `json:"target"`
	State         string          `json:"state"`
	Summary       string          `json:"summary"`
	Postcondition previewCheck    `json:"postcondition"`
	Receipt       any             `json:"receipt"`
	Next          []previewStep   `json:"next"`
}

type previewAudience struct {
	Only bool   `json:"only"`
	Who  string `json:"who"`
}

type previewCheck struct {
	Checked bool   `json:"checked"`
	Holds   *bool  `json:"holds"`
	How     string `json:"how"`
}

type previewStep struct {
	Command string `json:"command"`
	Why     string `json:"why"`
}

// missingWhyError is the refusal for a write that names no reason, as a real write refuses it.
func missingWhyError() error {
	return &errs.Coded{Code: "write_request_problem", Exit: errs.ExitUsage,
		Message: "the directed write was refused: " + string(da.CodeMissingReason) + "; nothing was sent", Fix: missingWhyFix}
}

// writeWithPreview sends the write, or with dryRun prints what it would do and who would see it. who names the
// audience (words that name who will see it), what the action is in words, and target the chat or mail it acts on.
func (rt *runtime) writeWithPreview(req da.Request, dryRun bool, who, what string, target *previewTarget) error {
	if !dryRun {
		return rt.directedWrite(req)
	}
	if req.Reason == "" {
		return missingWhyError()
	}
	if target != nil && strings.TrimSpace(target.ID) == "" {
		return &errs.Coded{Code: "write_request_problem", Exit: errs.ExitUsage,
			Message: "the directed write needs the " + target.Kind + " it acts on; nothing was previewed",
			Fix:     "Name the " + target.Kind + " with its id, as `m365crawl writes target --help` shows, then preview again."}
	}
	if strings.TrimSpace(who) == "" {
		who = "the recipients you name with --to"
	}
	if req.Content.Text != "" {
		what = fmt.Sprintf("%s: %q", what, req.Content.Text)
	}
	p := writePreview{
		Ax:            "outcome/1",
		Action:        req.Action,
		Effect:        "others",
		Audience:      previewAudience{Only: false, Who: who},
		Target:        target,
		State:         "planned",
		Summary:       fmt.Sprintf("Would %s, for %s; they will see it. Nothing was sent or changed.", what, who),
		Postcondition: previewCheck{Checked: false, Holds: nil, How: "dry run: nothing was sent or changed"},
		Next:          []previewStep{{Command: "m365crawl writes status", Why: "check the writes switch before sending for real"}},
	}
	return rt.previewWrite(p)
}

// previewWrite prints the preview: its summary on a terminal, else the outcome as one JSON document.
func (rt *runtime) previewWrite(p writePreview) error {
	if rt.format == output.Text {
		_, err := fmt.Fprintln(rt.stdout, p.Summary)
		return err
	}
	return rt.write("writes_preview", p)
}

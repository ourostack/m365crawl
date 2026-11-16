package cli

import (
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/errs"
)

// consentOf gives each command the consent class its declaration in actions/m365crawl.actions.mts holds: read
// changes nothing, write changes only the person's own archive, consequential reaches other people. The test
// checks this table against the generated commands.json.
var consentOf = map[string]string{
	"overview": "read", "doctor": "read", "sync": "write", "status": "read", "search": "read",
	"messages": "read", "conversations": "read", "teams": "read", "people list": "read",
	"people sync": "write", "people show": "read", "people sources": "read", "activity": "read",
	"coverage": "read", "calendar agenda": "read", "calendar event": "read", "calendar sources": "read",
	"calendar actions": "read", "calendar attendees sync": "write", "calendar attendees sources": "read",
	"calendar attendees list": "read", "calendar attendees show": "read", "mail list": "read",
	"mail show": "read", "mail thread": "read", "mail folders": "read", "mail unread": "read",
	"workiq enable": "write", "workiq disable": "write", "workiq sync": "write", "workiq coverage": "read",
	"workiq status": "read", "writes on": "write", "writes off": "write", "writes status": "read",
	"writes target": "read", "writes send-teams-message": "consequential", "writes save-mail-draft": "write",
	"writes reply-mail": "consequential", "writes reply-all-mail": "consequential", "writes send-mail": "consequential",
	"writes discard-mail-draft": "write", "writes add-reaction": "consequential",
	"writes remove-reaction": "consequential", "writes verify": "write", "writes settle": "write",
	"notes sync": "write", "notes list": "read", "notes search": "read", "notes show": "read",
	"notes sources": "read", "docs sync": "write", "docs list": "read", "docs search": "read",
	"docs show": "read", "docs sources": "read", "loop sync": "write", "loop list": "read",
	"loop search": "read", "loop show": "read", "loop sources": "read", "scout sync": "write",
	"scout list": "read", "scout search": "read", "scout show": "read", "scout sources": "read",
	"drafts list": "read", "drafts show": "read", "content images": "read", "content capture": "write",
	"content fetch image": "write", "content fetch engage-thread": "write", "content fetch engage-home": "write",
	"content fetch page": "write", "content fetch stream": "write", "content fetch doc": "write",
	"content signin": "write", "content list": "read", "content search": "read", "content show": "read",
	"content sources": "read", "transcripts list": "read", "transcripts show": "read",
	"transcripts fetch": "write", "transcripts signin": "write", "stores": "read", "records": "read",
	"unread": "read", "thread": "read", "watch": "read", "whoami": "read", "sql": "read", "skill": "read",
	"metadata": "read", "version": "read", "help": "read",
}

// exitCodes are every status the CLI exits with, as help --json documents them.
var exitCodes = []exitCode{
	{Code: exitOK, Name: "done", Meaning: "The command did what was asked, or the answer is empty."},
	{Code: errs.ExitRuntime, Name: "failed", Meaning: "The run failed; the error line says why and what to do next."},
	{Code: errs.ExitUsage, Name: "usage", Meaning: "The request is wrong: an unknown command or flag, or a missing argument. Nothing ran."},
	{Code: errs.ExitEnvironment, Name: "environment", Meaning: "This machine is not ready (Teams, Full Disk Access, the archive), or the person must act."},
	{Code: errs.ExitLocked, Name: "locked", Meaning: "Another m365crawl process holds the archive; retry when it finishes."},
	{Code: 5, Name: "unknown", Meaning: "A write whose outcome is unknown. Do not resend it; check with writes verify or tell the person."},
	{Code: exitForced, Name: "forced", Meaning: "A second interrupt ended the run at once."},
}

type exitCode struct {
	Code    int    `json:"code"`
	Name    string `json:"name"`
	Meaning string `json:"meaning"`
}

// helpCommand is one command as help --json lists it: its words, who sees its effect, and its usage.
type helpCommand struct {
	Name     string   `json:"name"`
	Summary  string   `json:"summary"`
	Effect   string   `json:"effect"`
	ReadOnly bool     `json:"readOnly,omitempty"`
	Usage    []string `json:"usage"`
}

type helpDocument struct {
	Tool      string        `json:"tool"`
	Commands  []helpCommand `json:"commands"`
	ExitCodes []exitCode    `json:"exitCodes"`
}

type helpCmd struct{}

// Run prints every command with who sees its effect, and the exit codes. JSON on request or in a pipe; a plain list
// on a terminal in text mode.
func (helpCmd) Run(rt *runtime, kctx *kong.Context) error {
	doc := helpDocument{Tool: "m365crawl", Commands: commandsOf(kctx.Model.Node), ExitCodes: exitCodes}
	if rt.format != output.Text {
		return rt.write("help", doc)
	}
	for _, c := range doc.Commands {
		_, _ = fmt.Fprintf(rt.stdout, "%s  %s\n", c.Name, c.Summary)
	}
	return nil
}

// writeUsage is the usage line of each write that names its target, so an agent (and the X-ray) sees which flags
// name the chat or mail the write acts on.
var writeUsage = map[string]string{
	"writes send-teams-message": "m365crawl writes send-teams-message --chat <chat> --text <text> --why <why>",
	"writes reply-mail":         "m365crawl writes reply-mail --message <message> --text <text> --why <why>",
	"writes reply-all-mail":     "m365crawl writes reply-all-mail --message <message> --text <text> --why <why>",
	"writes send-mail":          "m365crawl writes send-mail --to <to> --subject <subject> --text <text> --why <why>",
	"writes add-reaction":       "m365crawl writes add-reaction --chat <chat> --message <message> --reaction <reaction> --why <why>",
	"writes remove-reaction":    "m365crawl writes remove-reaction --chat <chat> --message <message> --reaction <reaction> --why <why>",
}

// commandsOf walks the parser's registered commands: every leaf, in registration order.
func commandsOf(root *kong.Node) []helpCommand {
	var out []helpCommand
	var walk func(n *kong.Node, words []string)
	walk = func(n *kong.Node, words []string) {
		for _, c := range n.Children {
			path := append(append([]string{}, words...), c.Name)
			if len(c.Children) > 0 {
				walk(c, path)
				continue
			}
			name := strings.Join(path, " ")
			consent := consentOf[name]
			effect := "private"
			if consent == "consequential" {
				effect = "others"
			}
			usage := "m365crawl " + name + " [flags]"
			if u, ok := writeUsage[name]; ok {
				usage = u
			}
			out = append(out, helpCommand{Name: name, Summary: c.Help, Effect: effect, ReadOnly: consent == "read", Usage: []string{usage}})
		}
	}
	walk(root, nil)
	return out
}

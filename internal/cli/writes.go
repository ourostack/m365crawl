package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/directedwrites"
	"github.com/ourostack/m365crawl/internal/errs"
)

type writesGroup struct {
	On               writesOnCmd     `cmd:"" help:"Turn writes on for this archive. Only the person, typing on the controlling terminal; never an agent."`
	Off              writesOffCmd    `cmd:"" help:"Turn writes off for this archive. Only the person, typing on the controlling terminal."`
	Status           writesStatusCmd `cmd:"" help:"Show the writes switch and the newest directed writes, offline."`
	Target           writesTargetCmd `cmd:"" help:"Read a chat's current recipients and version through WorkIQ, the values a send must name; writes nothing."`
	SendTeamsMessage writesSendCmd   `cmd:"" name:"send-teams-message" help:"Send a plain-text Teams chat message the person directed, through WorkIQ."`
	SaveMailDraft    writesDraftCmd  `cmd:"" name:"save-mail-draft" help:"Save a new plain-text mail draft the person directed in their own mailbox, through WorkIQ."`
	Verify           writesVerifyCmd `cmd:"" help:"Re-read the server to settle a write whose outcome is unknown; writes nothing to the server."`
	Settle           writesSettleCmd `cmd:"" help:"The person settles an unknown write after checking it, typing its id on the controlling terminal."`
}

func (writesGroup) Help() string {
	return "m365crawl is read-only unless the person turns writes on. While writes are on and the person directs an agent, the agent acts through these commands with no second confirmation (contracts/directed-actions/v0). Every write names the agent and a reason; a send to a chat names the acting identity and the chat's exact recipients (`writes target` prints them), and may name the version it read and an intent key, so a retry replays instead of sending twice.\n" +
		"Each write command prints one object: decision (accept, refuse or replay), outcome (sent, failed or unknown) and exit_class. Exit 0 done, 1 failed, 2 request problem, 3 person needed, 5 unknown. Never resend a write whose outcome is unknown: tell the person. `writes verify ID` may settle it by reading the server, otherwise the person checks and runs `writes settle`.\n" +
		"Only a Teams chat message and a new mail draft are supported. The gates stop accidental writes through m365crawl; they are not a security boundary against an agent with shell access."
}

func (writesOnCmd) Help() string     { return (writesGroup{}).Help() }
func (writesOffCmd) Help() string    { return (writesGroup{}).Help() }
func (writesStatusCmd) Help() string { return (writesGroup{}).Help() }
func (writesTargetCmd) Help() string { return (writesGroup{}).Help() }
func (writesSendCmd) Help() string   { return (writesGroup{}).Help() }
func (writesDraftCmd) Help() string  { return (writesGroup{}).Help() }
func (writesVerifyCmd) Help() string { return (writesGroup{}).Help() }
func (writesSettleCmd) Help() string { return (writesGroup{}).Help() }
func (rt *runtime) writesOptions() directedwrites.Options {
	return directedwrites.Options{DBPath: rt.dbPath, Invocation: rt.workIQInvocation(), Version: version}
}

// openTerminal opens the controlling terminal; a test seam.
var openTerminal = controllingTerminal

func terminalRequired(why string) error {
	return &errs.Coded{Code: "write_terminal_required", Exit: errs.ExitEnvironment, Message: why + "; nothing changed",
		Fix: "Only the person can do this, by running the command in their own terminal and typing the confirmation."}
}

// confirmOnTerminal asks the person on the controlling terminal, never standard input, to type want.
func confirmOnTerminal(prompt, want string) error {
	term, err := openTerminal()
	if err != nil {
		return terminalRequired("there is no controlling terminal")
	}
	defer func() { _ = term.Close() }()
	if _, err := fmt.Fprint(term, prompt); err != nil {
		return terminalRequired("the terminal could not be written")
	}
	line, _ := bufio.NewReader(io.LimitReader(term, 1024)).ReadString('\n')
	if strings.TrimSpace(line) != want {
		return terminalRequired("the typed confirmation did not match")
	}
	return nil
}

type writesOnCmd struct{}
type writesOffCmd struct{}

type writesSwitchResult struct {
	Writes string `json:"writes"`
}

func (r writesSwitchResult) renderWrites(rt *runtime) error {
	_, err := fmt.Fprintf(rt.stdout, "writes: %s\n", r.Writes)
	return err
}

func (writesOnCmd) Run(rt *runtime) error  { return rt.setWrites(true) }
func (writesOffCmd) Run(rt *runtime) error { return rt.setWrites(false) }

func (rt *runtime) setWrites(on bool) error {
	word, prompt := "off", "Turn m365crawl writes off for this archive. Type off to confirm: "
	if on {
		word, prompt = "on", "Agents you direct will send Teams messages and save mail drafts as you through WorkIQ, without asking again. Type on to confirm: "
	}
	if _, err := os.Stat(rt.dbPath); err != nil {
		return errs.Usage("there is no archive at the selected path; run `m365crawl sync` first, or pass the archive with --db")
	}
	if err := confirmOnTerminal(prompt, word); err != nil {
		return err
	}
	if _, err := directedwrites.SetSwitch(rt.dbPath, on); err != nil {
		return err
	}
	return rt.write(rt.cmd, writesSwitchResult{Writes: word})
}

type writesStatusCmd struct {
	Limit int `default:"20" help:"How many of the newest writes to list."`
}

type writesStatusResult struct{ directedwrites.StatusResult }

func (c writesStatusCmd) Run(rt *runtime) error {
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	result, err := directedwrites.Status(rt.ctx, rt.writesOptions(), c.Limit)
	if err != nil {
		return asCoded(err)
	}
	return rt.write(rt.cmd, writesStatusResult{result})
}

func (r writesStatusResult) renderWrites(rt *runtime) error {
	var out bytes.Buffer
	fmt.Fprintf(&out, "writes: %s; unknown outcomes: %d\n", r.Writes, r.Unknown)
	for _, w := range r.Items {
		fmt.Fprintf(&out, "%s %s %s %s by %s: %s\n", w.RequestID, stamp(w.CreatedAt), w.Action, w.Outcome, safeLine(w.Agent), safeLine(w.Reason))
		if w.Conversation != "" {
			fmt.Fprintf(&out, "  chat %s\n", safeLine(w.Conversation))
		}
		fmt.Fprintf(&out, "  %s\n", safeLine(w.Text))
	}
	_, err := io.Copy(rt.stdout, &out)
	return err
}

// safeLine shows text an agent supplied on one line, with control and format characters escaped,
// so it cannot drive the person's terminal.
func safeLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r >= 0x7f && r <= 0x9f || r >= 0x200b && r <= 0x200f || r >= 0x2028 && r <= 0x202e ||
			r >= 0x2060 && r <= 0x2069 || r == 0xfeff || r == 0xad || r >= 0xe0000 && r <= 0xe007f:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

type writesTargetCmd struct {
	Chat     string `required:"" help:"The chat id: 48:notes (Notes to self) or a 19: chat id."`
	Identity string `help:"The account to act as; default WorkIQ's signed-in account."`
}

type writesTargetResult struct{ directedwrites.TargetResult }

func (c writesTargetCmd) Run(rt *runtime) error {
	result, err := directedwrites.Target(rt.ctx, rt.writesOptions(), c.Chat, c.Identity)
	if err != nil {
		return asCoded(err)
	}
	return rt.write(rt.cmd, writesTargetResult{result})
}

func (r writesTargetResult) renderWrites(rt *runtime) error {
	if !r.Known {
		_, err := fmt.Fprintf(rt.stdout, "chat %s: not a chat a write can name (unknown_target)\n", safeLine(r.Chat))
		return err
	}
	_, err := fmt.Fprintf(rt.stdout, "chat %s as %s\nrecipients: %s\nversion: %s\n", safeLine(r.Chat), r.Identity,
		safeLine(strings.Join(r.Recipients, " ")), r.Version)
	return err
}

// writesRequest holds the flags every directed write shares. None is required by the parser, so a
// missing one gets the contract's own refusal.
type writesRequest struct {
	Agent    string   `help:"The agent that is acting (recorded)."`
	Reason   string   `help:"Why the agent acts (recorded)."`
	Identity string   `help:"The account that acts: the signed-in account's address. Required for a chat message."`
	To       []string `help:"A recipient address; repeat for each. For a chat message: every member, as writes target prints them."`
	Text     string   `help:"The plain text to write."`
	TextFile string   `name:"text-file" help:"Read the text from this file, or from standard input with -." placeholder:"PATH"`
}

func (w writesRequest) text() (string, error) {
	switch {
	case w.TextFile != "" && w.Text != "":
		return "", errs.Usage("give --text or --text-file, not both")
	case w.TextFile == "-":
		raw, err := io.ReadAll(io.LimitReader(stdinReader, 1<<20))
		return string(raw), err
	case w.TextFile != "":
		raw, err := os.ReadFile(w.TextFile) //nolint:gosec // G304: the caller's own text file.
		if err != nil {
			return "", errs.Usage("cannot read --text-file: " + err.Error())
		}
		return string(raw), nil
	}
	return w.Text, nil
}

func (w writesRequest) request(action string) (da.Request, error) {
	text, err := w.text()
	return da.Request{Contract: da.Contract, Action: action, Agent: w.Agent, Reason: w.Reason, Identity: w.Identity,
		Target: da.Target{Recipients: w.To}, Content: da.Content{Text: text}}, err
}

type writesSendCmd struct {
	writesRequest
	Chat            string `help:"The chat id: 48:notes (Notes to self) or a 19: chat id."`
	IntentKey       string `name:"intent-key" help:"A key for this send: asking again with the same key replays the earlier send instead of sending twice."`
	ExpectedVersion string `name:"expected-version" help:"The chat version the agent read (writes target prints it); refused if the chat has moved on."`
}

type writesDraftCmd struct {
	writesRequest
	Subject string `help:"The draft's subject."`
}

func (c writesSendCmd) Run(rt *runtime) error {
	req, err := c.request(directedwrites.SendTeamsMessage)
	if err != nil {
		return err
	}
	req.Target.Conversation, req.IntentKey, req.ExpectedVersion = c.Chat, c.IntentKey, c.ExpectedVersion
	return rt.directedWrite(req)
}

func (c writesDraftCmd) Run(rt *runtime) error {
	req, err := c.request(directedwrites.SaveMailDraft)
	if err != nil {
		return err
	}
	req.Content.Subject = c.Subject
	return rt.directedWrite(req)
}

// sendDirected is the directed send; a test seam.
var sendDirected = directedwrites.Send

func (rt *runtime) directedWrite(req da.Request) error {
	result, err := sendDirected(rt.ctx, rt.writesOptions(), req)
	if err != nil {
		return asCoded(err)
	}
	return rt.writeOutcome(writesOutcome{result})
}

type writesOutcome struct{ directedwrites.Result }

// writeOutcome prints the object, then the error line its exit class needs.
func (rt *runtime) writeOutcome(r writesOutcome) error {
	if err := rt.write(rt.cmd, r); err != nil {
		return err
	}
	return outcomeError(r.Result)
}

func outcomeError(r directedwrites.Result) error {
	what := string(r.Code)
	if r.Field != "" {
		what += " (" + r.Field + ")"
	}
	if what == "" {
		what = string(r.Outcome)
	}
	switch r.ExitClass {
	case da.ExitRequestProblem:
		return &errs.Coded{Code: "write_request_problem", Exit: errs.ExitUsage, Message: "the directed write was refused: " + what + "; nothing was sent",
			Fix: "Fix the request as the printed code and field say, then ask again."}
	case da.ExitPersonNeeded:
		return &errs.Coded{Code: "write_person_needed", Exit: errs.ExitEnvironment, Message: "the directed write was refused: " + what + "; nothing was sent",
			Fix: "Stop and tell the person: only they can turn writes on or settle the earlier write."}
	case da.ExitFailed:
		return &errs.Coded{Code: "write_failed", Exit: errs.ExitRuntime, Message: "the directed write did not happen",
			Fix: "Fix the problem the printed object names, then ask again."}
	case da.ExitUnknown:
		return &errs.Coded{Code: "write_outcome_unknown", Exit: exitWriteUnknown, Message: "m365crawl cannot tell whether the directed write happened",
			Fix: "Do not send it again. Tell the person; `m365crawl writes verify " + r.RequestID + "` may settle it, otherwise the person checks and runs `m365crawl writes settle " + r.RequestID + "`."}
	}
	return nil
}

// exitWriteUnknown is the exit status of a directed write that may have happened.
const exitWriteUnknown = 5

func (r writesOutcome) renderWrites(rt *runtime) error {
	var out bytes.Buffer
	switch {
	case r.Decision == da.Refuse:
		fmt.Fprintf(&out, "%s refused: %s", r.Action, r.Code)
		if r.Field != "" {
			fmt.Fprintf(&out, " (%s)", r.Field)
		}
		out.WriteString("; nothing was sent\n")
	case r.Decision == da.Replay:
		fmt.Fprintf(&out, "%s replayed write %s (%s); nothing was sent again\n", r.Action, r.ReplayOf, r.EarlierOutcome)
	default:
		what := map[da.Outcome]string{da.Sent: "sent", da.Failed: "failed: it did not happen", da.Unknown: "outcome unknown: do not send it again"}[r.Outcome]
		if r.Action == directedwrites.SaveMailDraft && r.Outcome == da.Sent {
			what = "draft saved"
		}
		fmt.Fprintf(&out, "write %s %s: %s\n", r.RequestID, r.Action, what)
		if r.ServerID != "" {
			fmt.Fprintf(&out, "  server id %s\n", safeLine(r.ServerID))
		}
		if r.Notice != "" {
			fmt.Fprintf(&out, "  note: %s\n", r.Notice)
		}
	}
	_, err := io.Copy(rt.stdout, &out)
	return err
}

type writesVerifyCmd struct {
	ID string `arg:"" help:"The write's request_id."`
}

type writesSettleCmd struct {
	ID      string `arg:"" help:"The write's request_id."`
	Sent    bool   `help:"The person checked: it happened."`
	NotSent bool   `name:"not-sent" help:"The person checked: it did not happen."`
}

func writeID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id < 1 {
		return 0, errs.Usage("a write id is the positive number writes status lists as request_id")
	}
	return id, nil
}

func (c writesVerifyCmd) Run(rt *runtime) error {
	id, err := writeID(c.ID)
	if err != nil {
		return err
	}
	result, err := directedwrites.Verify(rt.ctx, rt.writesOptions(), id)
	if err != nil {
		return asCoded(err)
	}
	return rt.writeOutcome(writesOutcome{result})
}

func (c writesSettleCmd) Run(rt *runtime) error {
	id, err := writeID(c.ID)
	if err != nil {
		return err
	}
	if c.Sent == c.NotSent {
		return errs.Usage("give exactly one of --sent and --not-sent")
	}
	if _, err := directedwrites.Write(rt.ctx, rt.writesOptions(), id); err != nil {
		return asCoded(err)
	}
	said := "did not happen"
	if c.Sent {
		said = "happened"
	}
	if err := confirmOnTerminal("You checked that directed write "+c.ID+" "+said+". Type its id to confirm: ", strconv.FormatInt(id, 10)); err != nil {
		return err
	}
	result, err := directedwrites.Settle(rt.ctx, rt.writesOptions(), id, c.Sent)
	if err != nil {
		return asCoded(err)
	}
	// The person's settlement succeeded; the printed outcome says what the write now is.
	return rt.write(rt.cmd, writesOutcome{result})
}

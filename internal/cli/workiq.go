package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/workiq"
)

type workIQGroup struct {
	Enable   workIQEnableCmd   `cmd:"" help:"Enable the optional source for this archive; no subprocess or sign-in."`
	Disable  workIQDisableCmd  `cmd:"" help:"Disable acquisition without removing retained data or progress."`
	Sync     workIQSyncCmd     `cmd:"" help:"Explicitly acquire mail or selected chat history through WorkIQ fetch."`
	Coverage workIQCoverageCmd `cmd:"" help:"Count WorkIQ folders/chat messages without retaining payloads or data cursors."`
	Status   workIQStatusCmd   `cmd:"" help:"Explicitly probe WorkIQ identity and initial path access, even when disabled; never sign in."`
}

func (workIQGroup) Help() string {
	return "WorkIQ is optional and disabled by default. A person installs it, signs in and accepts its EULA once, through WorkIQ; m365crawl never logs in or accepts an agreement. Only these explicit commands acquire or probe server data; ordinary reads, sync and watch stay cache-only. A read that reaches before the cache lists them in workiq_routes, and agents may run them on their own.\n" +
		"Mail needs an already archived Outlook profile. --account outlook/<profile> selects it, not --outlook-account. Stored identity mismatches and several content mailboxes are refused. Chat message IDs and cross-source clocks remain unqualified: count with a gap, do not insert unverified message rows.\n" +
		"Coverage discards payloads and never advances data cursors or covering success. Status tests initial listing paths only, not every content or pagination path. Enabled doctor runs version only. M365CRAWL_WORKIQ_BIN selects the executable, and is required with explicit source roots or under tests."
}

type workIQEnableCmd struct{}
type workIQDisableCmd struct{}

type workIQToggleResult struct {
	Enabled bool `json:"enabled"`
}

func (workIQEnableCmd) Run(rt *runtime) error  { return rt.setWorkIQ(true) }
func (workIQDisableCmd) Run(rt *runtime) error { return rt.setWorkIQ(false) }

func (rt *runtime) checkWorkIQLinks() error {
	if rt.g.OutlookAccount != "" || rt.g.OutlookProfile != "" {
		return errs.Usage("WorkIQ does not change Outlook links; use --account to select an existing archive account")
	}
	return nil
}

func (rt *runtime) setWorkIQ(enabled bool) error {
	if err := rt.checkWorkIQLinks(); err != nil {
		return err
	}
	err := store.WithWorkIQWriter(rt.ctx, rt.dbPath, func(ctx context.Context, s *store.Store) error {
		return s.SetWorkIQEnabled(ctx, enabled)
	})
	if err != nil {
		return asCoded(err)
	}
	return rt.write(rt.cmd, workIQToggleResult{Enabled: enabled})
}

func (r workIQToggleResult) renderWorkIQ(rt *runtime) error {
	_, err := fmt.Fprintf(rt.stdout, "WorkIQ enabled: %t\n", r.Enabled)
	return err
}

type workIQReadFlags struct {
	Source   string `enum:"all,mail,chats" default:"all" help:"WorkIQ source family."`
	Since    string `help:"Chat window start: RFC3339, YYYY-MM-DD or a relative duration such as 365d; default 365 days. Not a mail filter."`
	MaxChats int    `default:"200" help:"Positive chat selection cap, not a coverage promise; at most 100000."`
}

type workIQSyncCmd struct{ workIQReadFlags }
type workIQCoverageCmd struct{ workIQReadFlags }
type workIQStatusCmd struct {
	Source string `enum:"all,mail,chats" default:"all" help:"WorkIQ source family to probe."`
}

func (workIQSyncCmd) Help() string     { return (workIQGroup{}).Help() }
func (workIQCoverageCmd) Help() string { return (workIQGroup{}).Help() }
func (workIQStatusCmd) Help() string   { return (workIQGroup{}).Help() }

type workIQExplicitFlags struct{ MaxChats bool }

func explicitWorkIQFlags(kctx *kong.Context) workIQExplicitFlags {
	var flags workIQExplicitFlags
	for _, path := range kctx.Path {
		if path.Flag != nil && !path.Resolved && path.Flag.Name == "max-chats" {
			flags.MaxChats = true
		}
	}
	return flags
}

func (c workIQSyncCmd) Run(rt *runtime, flags workIQExplicitFlags) error {
	return rt.runWorkIQ("sync", c.workIQReadFlags, flags)
}

func (c workIQCoverageCmd) Run(rt *runtime, flags workIQExplicitFlags) error {
	return rt.runWorkIQ("coverage", c.workIQReadFlags, flags)
}

func (c workIQStatusCmd) Run(rt *runtime) error {
	return rt.runWorkIQ("status", workIQReadFlags{Source: c.Source, MaxChats: 200}, workIQExplicitFlags{})
}

// runWorkIQSyncer is the explicit WorkIQ run; a test seam.
var runWorkIQSyncer = syncer.RunWorkIQ

type workIQResult struct {
	Status string `json:"status"`
	// Resumable is the syncer's own judgment, used as-is: true only when a mail sync stopped at its
	// page budget and the next run continues from the kept position.
	Resumable bool `json:"resumable"`
	syncer.WorkIQReport
}

func (rt *runtime) workIQInvocation() workiq.Invocation {
	return workiq.Invocation{ExplicitTeamsRoot: rt.g.TeamsRoot != "", ExplicitOutlookRoot: rt.g.OutlookRoot != ""}
}

func (rt *runtime) runWorkIQ(kind string, flags workIQReadFlags, explicit workIQExplicitFlags) error {
	if err := rt.checkWorkIQLinks(); err != nil {
		return err
	}
	mailOnly := flags.Source == "mail" || strings.HasPrefix(rt.g.Account, "outlook/")
	if mailOnly && (flags.Since != "" || explicit.MaxChats) {
		return errs.Usage("--since and --max-chats select chats, not mail")
	}
	if flags.MaxChats < 1 || flags.MaxChats > 100000 {
		return errs.Usage("--max-chats must be between 1 and 100000")
	}
	if rt.g.Account != "" && flags.Source != "all" &&
		(strings.HasPrefix(rt.g.Account, "outlook/") != (flags.Source == "mail")) {
		return errs.Usage("--account conflicts with the selected WorkIQ source")
	}
	since, err := rt.when("--since", flags.Since)
	if err != nil {
		return err
	}
	report, err := runWorkIQSyncer(rt.ctx, syncer.WorkIQOptions{DBPath: rt.dbPath, Source: flags.Source,
		Account: rt.g.Account, Kind: kind, Since: since, MaxChats: flags.MaxChats, Invocation: rt.workIQInvocation()})
	report.Kind = kind
	if report.Accounts == nil {
		report.Accounts = []syncer.WorkIQAccountReport{}
	}
	if report.Mail == nil {
		report.Mail = []syncer.WorkIQMailCoverage{}
	}
	if report.Chats == nil {
		report.Chats = []syncer.WorkIQChatCoverage{}
	}
	if report.Probes == nil {
		report.Probes = []syncer.WorkIQPathProbe{}
	}
	for i, probe := range report.Probes {
		if probe.State == "account_required" {
			report.Probes[i].Next = rt.recoveryCommand(false, probe.Account, "workiq", "status", "--source", probe.Family)
		}
	}
	status := "ok"
	if len(report.Gaps) > 0 {
		status = "ok_with_omissions"
	}
	if err != nil {
		if len(report.Accounts)+len(report.Mail)+len(report.Chats)+len(report.Probes) == 0 {
			return asCoded(err)
		}
		status = "partial"
	}
	if writeErr := rt.write(rt.cmd, workIQResult{Status: status, Resumable: report.Resumable, WorkIQReport: report}); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return asCoded(err)
	}
	return nil
}

func (r workIQResult) renderWorkIQ(rt *runtime) error {
	var out bytes.Buffer
	fmt.Fprintf(&out, "WorkIQ %s: %s; enabled %t\n", r.Kind, r.Status, r.Enabled)
	if r.Resumable {
		fmt.Fprintf(&out, "resumable: mail sync kept its position; run the same command again to continue\n")
	}
	for _, account := range r.Accounts {
		fmt.Fprintf(&out, "%s %s: %s\n", account.Family, oneLine(account.Account), account.Status)
	}
	for _, folder := range r.Mail {
		total := "unknown"
		if folder.ServerTotal != nil {
			total = fmt.Sprint(*folder.ServerTotal)
		}
		fmt.Fprintf(&out, "mail folder %s: server total %s; current overlap unknown (not a windowed count)\n", oneLine(folder.FolderID), total)
	}
	for _, chat := range r.Chats {
		fmt.Fprintf(&out, "chat %s: server all-type count %d; archive comparable count %d; complete %t; %s to %s\n",
			oneLine(chat.Conversation), chat.ServerCount, chat.ArchiveCount, chat.Complete, stamp(chat.Since), stamp(chat.Until))
	}
	for _, probe := range r.Probes {
		fmt.Fprintf(&out, "%s path %s: %s; initial page only\n", probe.Family, probe.PathClass, probe.State)
		if probe.Next != "" {
			fmt.Fprintf(&out, "  next: %s\n", probe.Next)
		}
	}
	keys := make([]string, 0, len(r.Gaps))
	for key := range r.Gaps {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&out, "gap %s: %d\n", key, r.Gaps[key])
	}
	_, err := io.Copy(rt.stdout, &out)
	return err
}

func (rt *runtime) workIQDoctorChecks() []check {
	st, err := openArchiveReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		return nil
	}
	if err != nil {
		return []check{{Name: "workiq", Detail: "cannot inspect optional source settings", Fix: "Check the selected archive before probing WorkIQ."}}
	}
	enabled, err := st.WorkIQEnabled(rt.ctx)
	err = errors.Join(err, st.Close())
	if err != nil {
		return []check{{Name: "workiq", Detail: "cannot inspect optional source settings", Fix: "Check the selected archive before probing WorkIQ."}}
	}
	if !enabled {
		return nil
	}
	version, err := workiq.Version(rt.ctx, rt.workIQInvocation())
	if err != nil {
		code := "workiq_failed"
		fix := "Inspect WorkIQ setup separately; no sign-in or agreement was performed."
		var coded *errs.Coded
		if errors.As(err, &coded) {
			code, fix = coded.Code, coded.Fix
		}
		return []check{{Name: "workiq", Detail: code, Fix: fix}}
	}
	return []check{{Name: "workiq", OK: true, Detail: "WorkIQ version " + version + "; sign-in and path access not probed"}}
}

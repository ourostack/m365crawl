package cli

import (
	"fmt"
	"os"
	goruntime "runtime"
	"strings"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

// Mail states of `status`: what the mail block says about reading mail on this machine.
const (
	mailStateOK                  = "ok"
	mailStateSkipped             = "skipped"
	mailStateUnsupportedPlatform = "unsupported_platform"
	mailStateNoProfile           = "no_profile"
	mailStateUnavailable         = "unavailable"
)

// Test seams: whether this operating system reads mail, and whether file modes mean anything on it.
var (
	mailSupported = func() bool {
		switch mailPlatform {
		case "darwin", "windows", "linux":
			return true
		default:
			return false
		}
	}
	archiveModeApplicable = func() bool { return goruntime.GOOS != "windows" }
)

// mailStatusBlock is the mail part of `status --json`.
type mailStatusBlock struct {
	store.MailStatus
	// State distinguishes archived read state from unavailable discovery; SourceError is fixed and never contains source values.
	State       string           `json:"state"`
	SourceError *mailSourceError `json:"source_error,omitempty"`
}

// mailStatus builds the block; st is nil when there is no archive.
func (rt *runtime) mailStatus(st *store.Store) (*mailStatusBlock, error) {
	return rt.mailStatusOf(st, "")
}

// mailStatusOf is mailStatus for one mail account, or every account when account is empty.
func (rt *runtime) mailStatusOf(st *store.Store, account string) (*mailStatusBlock, error) {
	b := &mailStatusBlock{}
	if st != nil {
		var err error
		if b.MailStatus, err = st.MailStatusOf(rt.ctx, account); err != nil {
			return nil, err
		}
	}
	switch {
	case !mailSupported():
		b.State = mailStateUnsupportedPlatform
	case !rt.outlookOn:
		b.State = mailStateSkipped
	default:
		profiles, problem := rt.outlookProfiles()
		switch {
		case problem != nil:
			b.State, b.SourceError = mailStateUnavailable, problem
		case !b.SyncedAt.IsZero():
			b.State = mailStateOK
		case len(profiles) == 0:
			b.State = mailStateNoProfile
		default:
			b.State = mailStateSkipped
		}
	}
	return b, nil
}

// text is the block as one line of `status`.
func (b *mailStatusBlock) text() string {
	if b.SourceError != nil {
		return b.State + ": " + mailSourceNote(b.SourceError)
	}
	if b.State != mailStateOK {
		return b.State
	}
	return fmt.Sprintf("%d messages, %d unread, %d folders; oldest %s; synced %s", b.Messages, b.Unread, b.Folders, stamp(b.OldestAt), stamp(b.SyncedAt))
}

// outlookProfiles retains healthy profiles alongside a fixed error; an unexamined source is never evidence of absence.
func (rt *runtime) outlookProfiles() ([]outlookdesktop.Profile, *mailSourceError) {
	s := rt.discoverOutlookMail()
	return s.profiles(), s.problem(rt.outlookDefault)
}

func (s outlookMailSource) profiles() []outlookdesktop.Profile {
	if s.owa {
		profiles := make([]outlookdesktop.Profile, len(s.native), len(s.native)+len(s.refused))
		for i, p := range s.native {
			profiles[i] = outlookdesktop.Profile{Name: p.Name, Dir: p.Dir}
		}
		for _, p := range s.refused {
			profiles = append(profiles, outlookdesktop.Profile{Name: p.Name, Dir: p.Dir})
		}
		return profiles
	}
	return s.legacy
}

// mailReadableCheck reports whether mail is being read: per Outlook profile, when the last read was
// or why it failed. It warns and never fails, like the Outlook store check: mail is optional and
// fails alone.
func (rt *runtime) mailReadableCheck(st *store.Store) check {
	const name = "mail_readable"
	switch {
	case !mailSupported():
		return check{Name: name, OK: true, Detail: "mail is not supported on this operating system"}
	case !rt.outlookOn:
		return check{Name: name, OK: true, Detail: "mail is not read: the Outlook source is off (--outlook-root none, or --teams-root without --outlook-root)"}
	case st == nil:
		return check{Name: name, OK: true, Detail: "no archive yet; the first sync reads mail"}
	}
	source := rt.discoverOutlookMail()
	problem := source.problem(rt.outlookDefault)
	if problem != nil {
		if source.owa {
			c := rt.outlookNativeCheck(source, st)
			c.Name = name
			return c
		}
		return check{Name: name, OK: true, Warn: true, Detail: mailSourceNote(problem), Fix: problem.Fix}
	}
	profiles := source.profiles()
	if len(profiles) == 0 {
		return check{Name: name, OK: true, Detail: "no new Outlook profile; no mail to read"}
	}
	var details, fixes []string
	for _, p := range profiles {
		// A Windows profile can hold several mailbox accounts; their combined state is the profile's.
		ms, mailboxes, err := st.OWAProfileMailState(rt.ctx, "outlook/"+p.Name)
		var stale *store.SourceHealth
		if err == nil {
			stale, err = rt.outlookMailStale(st, mailboxes)
		}
		several := ""
		if len(mailboxes) > 1 {
			several = fmt.Sprintf(" (%d mailboxes)", len(mailboxes))
		}
		switch {
		case err != nil:
			details, fixes = append(details, "profile "+p.Name+": cannot read the mail state: "+err.Error()), append(fixes, "Run `m365crawl sync`.")
		case ms.Failure != nil:
			details, fixes = append(details, "profile "+p.Name+": the last mail read failed: "+ms.Failure.Code+": "+ms.Failure.Message+several), append(fixes, firstOf(ms.Failure.Fix, "Run `m365crawl sync` and read its error."))
		case ms.Read && stale != nil:
			details, fixes = append(details, "profile "+p.Name+": mail read "+oneUnit(rt.now().Sub(ms.ReadAt))+" ago"+several+", but "+outlookStaleDetail(*stale)), append(fixes, outlookStaleFix)
		case ms.Read:
			details, fixes = append(details, "profile "+p.Name+": mail read "+oneUnit(rt.now().Sub(ms.ReadAt))+" ago"+several), append(fixes, "")
		default:
			details, fixes = append(details, "profile "+p.Name+": mail not read yet"+several), append(fixes, "Run `m365crawl sync`.")
		}
	}
	c := check{Name: name, OK: true, Detail: strings.Join(details, "; ")}
	for _, f := range fixes {
		if f != "" {
			c.Warn, c.Fix = true, f
			break
		}
	}
	return c
}

// mailArchiveModeCheck warns when other users can read the archive: it holds mail. It reads the
// mode of the file and nothing of its content.
func (rt *runtime) mailArchiveModeCheck() check {
	const name = "mail_archive_mode"
	if !archiveModeApplicable() {
		return check{Name: name, OK: true, Detail: "not applicable on Windows"}
	}
	info, err := os.Stat(rt.dbPath)
	switch {
	case err != nil:
		return check{Name: name, OK: true, Detail: "no archive yet; the first sync creates it with mode 0600"}
	case info.Mode().Perm()&0o077 != 0:
		return check{Name: name, OK: true, Warn: true, Detail: fmt.Sprintf("the archive has mode %04o: other users can read the mail in it", info.Mode().Perm()), Fix: "Run `chmod 600 " + rt.dbPath + "`."}
	}
	return check{Name: name, OK: true, Detail: "the archive is readable by its owner only (0600)"}
}

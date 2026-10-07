package cli

import (
	"fmt"
	"os"
	goruntime "runtime"
	"strings"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

// Mail states of `status`: what the mail block says about reading mail on this machine.
const (
	mailStateOK                  = "ok"
	mailStateSkipped             = "skipped"
	mailStateUnsupportedPlatform = "unsupported_platform"
	mailStateNoProfile           = "no_profile"
)

// Test seams: whether this operating system reads mail, and whether file modes mean anything on it.
var (
	mailSupported         = func() bool { return goruntime.GOOS != "windows" }
	archiveModeApplicable = func() bool { return goruntime.GOOS != "windows" }
)

// mailStatusBlock is the mail part of `status --json`.
type mailStatusBlock struct {
	store.MailStatus
	// State is ok (mail was read), skipped (the Outlook source is off for this run, or no sync has
	// read the mail yet), unsupported_platform or no_profile (no Outlook profile on this machine).
	State string `json:"state"`
}

// mailStatus builds the block; st is nil when there is no archive.
func (rt *runtime) mailStatus(st *store.Store) (*mailStatusBlock, error) {
	b := &mailStatusBlock{}
	if st != nil {
		var err error
		if b.MailStatus, err = st.MailStatus(rt.ctx); err != nil {
			return nil, err
		}
	}
	switch {
	case !mailSupported():
		b.State = mailStateUnsupportedPlatform
	case !rt.outlookOn:
		b.State = mailStateSkipped
	case !b.SyncedAt.IsZero():
		b.State = mailStateOK
	case len(rt.outlookProfiles()) == 0:
		b.State = mailStateNoProfile
	default:
		b.State = mailStateSkipped
	}
	return b, nil
}

// text is the block as one line of `status`.
func (b *mailStatusBlock) text() string {
	if b.State != mailStateOK {
		return b.State
	}
	return fmt.Sprintf("%d messages, %d unread, %d folders; oldest %s; synced %s", b.Messages, b.Unread, b.Folders, stamp(b.OldestAt), stamp(b.SyncedAt))
}

// outlookProfiles lists the profiles of the Outlook root this run reads, none when there is no
// root or it cannot be listed.
func (rt *runtime) outlookProfiles() []outlookdesktop.Profile {
	root := rt.outlookRoot
	if root == "" {
		var err error
		if root, err = outlookDefaultRoot(); err != nil {
			return nil
		}
	}
	profiles, _, _, _ := outlookDiscover(root)
	return profiles
}

// mailReadableCheck reports whether mail is being read: per Outlook profile, when the last read was
// or why it failed. It warns and never fails, like the Outlook store check: mail is optional and
// fails alone.
func (rt *runtime) mailReadableCheck(st *store.Store) check {
	const name = "mail_readable"
	switch {
	case !mailSupported():
		c := errs.MailUnsupportedPlatform()
		return check{Name: name, OK: true, Detail: c.Message}
	case !rt.outlookOn:
		return check{Name: name, OK: true, Detail: "mail is not read: the Outlook source is off (--outlook-root none, or --teams-root without --outlook-root)"}
	case st == nil:
		return check{Name: name, OK: true, Detail: "no archive yet; the first sync reads mail"}
	}
	profiles := rt.outlookProfiles()
	if len(profiles) == 0 {
		return check{Name: name, OK: true, Detail: "no new Outlook profile; no mail to read"}
	}
	var details, fixes []string
	for _, p := range profiles {
		ms, err := st.MailState(rt.ctx, "outlook/"+p.Name)
		switch {
		case err != nil:
			details, fixes = append(details, "profile "+p.Name+": cannot read the mail state: "+err.Error()), append(fixes, "Run `m365crawl sync`.")
		case ms.Failure != nil:
			details, fixes = append(details, "profile "+p.Name+": the last mail read failed: "+ms.Failure.Code+": "+ms.Failure.Message), append(fixes, firstOf(ms.Failure.Fix, "Run `m365crawl sync` and read its error."))
		case ms.Read:
			details, fixes = append(details, "profile "+p.Name+": mail read "+oneUnit(rt.now().Sub(ms.ReadAt))+" ago"), append(fixes, "")
		default:
			details, fixes = append(details, "profile "+p.Name+": mail not read yet"), append(fixes, "Run `m365crawl sync`.")
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

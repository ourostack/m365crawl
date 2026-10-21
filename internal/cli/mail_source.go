package cli

import (
	"errors"
	"io/fs"
	"strings"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

var (
	outlookNativeDefaultRoot = outlookowa.DefaultRoot
	outlookNativeDiscover    = outlookowa.Discover
)

func defaultMailRoot() (string, error) {
	root, err := outlookNativeDefaultRoot()
	if errors.Is(err, outlookdesktop.ErrNotSupported) {
		return outlookdesktop.DefaultRoot()
	}
	return root, err
}

type outlookMailSource struct {
	root    string
	owa     bool
	legacy  []outlookdesktop.Profile
	classic []string
	skipped []outlookdesktop.SkippedProfile
	native  []outlookowa.Profile
	refused []outlookowa.SkippedProfile
	err     error
}

type mailSourceError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

// Match the runner: native Windows defaults, positively recognized explicit HxStore first, then supported explicit OWA roots. Discovery examines paths and metadata, never values.
func (rt *runtime) discoverOutlookMail() outlookMailSource {
	s := outlookMailSource{root: rt.outlookRoot}
	if s.root == "" {
		s.owa = mailPlatform == "windows"
		s.root, s.err = outlookDefaultRoot()
		if s.err != nil {
			return s
		}
	}
	if !s.owa {
		s.legacy, s.classic, s.skipped, s.err = outlookDiscover(s.root)
		if len(s.legacy)+len(s.classic) > 0 {
			return s
		}
	}
	profiles, skipped, err := outlookNativeDiscover(s.root)
	if s.owa || len(profiles)+len(skipped) > 0 || err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.owa, s.native, s.refused, s.err = true, profiles, skipped, err
	}
	return s
}

// Resolved optional absence and unsupported legacy defaults are nonwarnings; failed resolution/enumeration or refused siblings remain unknown.
func (s outlookMailSource) problem(optional bool) *mailSourceError {
	if optional && !s.owa && s.root == "" && errors.Is(s.err, outlookdesktop.ErrNotSupported) {
		return nil
	}
	if s.err != nil && optional && s.root != "" && (errors.Is(s.err, fs.ErrNotExist) || errors.Is(s.err, outlookdesktop.ErrRootNotFound)) {
		return nil
	}
	if s.err != nil || len(s.refused)+len(s.skipped) > 0 {
		return &mailSourceError{Code: "outlook_profile_unreadable",
			Message: "the Outlook mail source could not be examined",
			Fix:     "Check that the Outlook cache root and each profile are readable, then run `m365crawl sync`."}
	}
	return nil
}

func mailSourceNote(problem *mailSourceError) string {
	return problem.Message + " (" + problem.Code + "); " + problem.Fix
}

func (rt *runtime) outlookNativeCheck(s outlookMailSource, st *store.Store) check {
	c := check{Name: "outlook_store", OK: true}
	switch {
	case s.err != nil && rt.outlookDefault && s.root != "" && errors.Is(s.err, fs.ErrNotExist):
		c.Detail = "the new Outlook OWA cache is not installed; nothing to read"
		return c
	case s.err != nil:
		c.Warn, c.Detail, c.Fix = true, "the Outlook OWA profile paths cannot be examined (outlook_profile_unreadable)", "Check that the Outlook cache directories are readable by your account."
		return c
	case len(s.native)+len(s.refused) == 0:
		c.Detail = "the new Outlook OWA cache has no profile; nothing to read"
		if !rt.outlookDefault {
			c.Warn, c.Fix = true, "Point --outlook-root at an EBWebView directory containing Outlook IndexedDB profiles."
		}
		return c
	}
	details := []string{"OWA cached calendar and mail; independent read state"}
	calendars := map[string]*store.OutlookSource{}
	calendarUnreadable := false
	if st != nil && len(s.native) > 0 {
		res, err := readCalendarSources(st, rt.ctx, store.CalendarSourcesFilter{Now: rt.now(), ReadInterval: syncer.OutlookMinReadInterval})
		calendarUnreadable = err != nil
		for _, row := range res.Rows {
			if row.Source == "outlook" {
				calendars[row.AccountID] = row.Outlook
			}
		}
	}
	for range s.refused {
		details = append(details, "a cached profile cannot be examined (outlook_profile_unreadable)")
		c.Warn, c.Fix = true, "Check that each Outlook cache profile is readable and has one supported origin."
	}
	for _, p := range s.native {
		detail := "profile " + p.Name + ": cached paths readable; mail not read yet"
		if st != nil {
			state, err := st.MailState(rt.ctx, "outlook/"+p.Name)
			switch {
			case err != nil:
				detail = "profile " + p.Name + ": cannot read the archived mail state"
				c.Warn, c.Fix = true, "Run `m365crawl sync`."
			case state.Failure != nil:
				detail = "profile " + p.Name + ": the last mail read failed (" + state.Failure.Code + ")"
				c.Warn, c.Fix = true, "Run `m365crawl sync` and read its coded error."
			case state.Read:
				detail = "profile " + p.Name + ": cached paths readable; mail read " + oneUnit(rt.now().Sub(state.ReadAt)) + " ago"
			}
		}
		calendarDetail := "calendar not read yet"
		switch o := calendars["outlook/"+p.Name]; {
		case calendarUnreadable:
			calendarDetail = "cannot read the archived calendar state"
			c.Warn, c.Fix = true, "Run `m365crawl sync`."
		case o != nil && o.Failure != nil:
			calendarDetail = "the last calendar read failed (" + o.Failure.Code + ")"
			c.Warn, c.Fix = true, "Run `m365crawl sync` and read its coded error."
		case o != nil && !o.LastReadAt.IsZero():
			calendarDetail = "calendar read " + oneUnit(rt.now().Sub(o.LastReadAt)) + " ago, " + o.Status
		}
		detail += "; " + calendarDetail
		details = append(details, detail)
	}
	c.Detail = strings.Join(details, "; ")
	return c
}

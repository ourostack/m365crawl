package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/sharepointcontent"
	"github.com/ourostack/m365crawl/internal/store"
)

type contentFetchOptions struct {
	URL            string        `arg:"" optional:"" help:"One supported HTTPS SharePoint page or Stream URL. Omit only with --registration-id."`
	RegistrationID int64         `help:"Retry an existing cloud registration (not a native source ID); mutually exclusive with URL and transcript selection."`
	Browser        string        `env:"M365CRAWL_BROWSER" help:"edge, chrome, or browser executable path; uses only the archive's tool-owned profile."`
	Timeout        time.Duration `default:"10m" help:"Bound the explicit one-target capture and publication."`
}
type contentFetchPageCmd struct{ contentFetchOptions }
type contentFetchStreamCmd struct {
	contentFetchOptions
	TranscriptID *string `help:"Exact requested native transcript ID; explicit empty is invalid. Absent allows backend selection only when unambiguous."`
}

func (c *contentFetchPageCmd) Run(rt *runtime) error {
	return c.run(rt, "page", nil)
}
func (c *contentFetchStreamCmd) Run(rt *runtime) error {
	return c.run(rt, "stream", c.TranscriptID)
}
func (contentFetchPageCmd) Help() string   { return contentHelp }
func (contentFetchStreamCmd) Help() string { return contentHelp }

type CloudFetchReport struct {
	RegistrationID        int64          `json:"registration_id"`
	SourceID              *int64         `json:"source_id"`
	ObservationID         *string        `json:"observation_id"`
	State                 string         `json:"state"`
	Scope                 string         `json:"scope"`
	Losses                map[string]int `json:"losses"`
	RetainedObservationID *string        `json:"retained_observation_id"`
	Partial               bool           `json:"partial"`
}

func (c *contentFetchOptions) run(rt *runtime, kind string, selection *string) error {
	if err := rt.contentFlags(); err != nil {
		return err
	}
	if c.Timeout <= 0 || c.RegistrationID < 0 || (c.URL == "") == (c.RegistrationID == 0) || (c.RegistrationID > 0 && selection != nil) || (selection != nil && *selection == "") {
		return errs.Usage("content fetch takes one URL or --registration-id, a positive timeout and a nonempty explicit transcript ID")
	}
	var request sharepointcontent.CanonicalRequest
	var err error
	if c.RegistrationID > 0 {
		st, openErr := store.OpenReadOnly(rt.ctx, rt.dbPath)
		if openErr != nil {
			return rt.contentError(openErr)
		}
		request, err = st.CloudRequest(rt.ctx, c.RegistrationID)
		err = errors.Join(err, st.Close())
		if err != nil {
			return rt.contentError(err)
		}
		if request.Request.Kind != kind {
			return errs.Usage("registration belongs to a different content kind")
		}
	} else {
		input := sharepointcontent.Request{Kind: kind, URL: c.URL}
		if selection != nil {
			input.TranscriptID = *selection
		}
		request, err = sharepointcontent.NormalizeRequest(input)
		if err != nil {
			return errs.Usage("unsupported content URL or transcript selection; see content --help")
		}
	}
	exe, browserKind, err := findBrowser(c.Browser)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(rt.ctx, c.Timeout)
	defer cancel()
	previous := rt.ctx
	rt.ctx = ctx
	defer func() { rt.ctx = previous }()
	report, err := rt.fetchCloud(request, browser.LaunchOptions{Exe: exe, Kind: browserKind, Profile: browser.ProfileDir(rt.dbPath), Headless: true})
	if err != nil {
		return rt.contentError(err)
	}
	return rt.write("content fetch "+kind, report)
}

// cloudWrite owns only the short archive lifetime, never a browser/profile lease.
func (rt *runtime) cloudWrite(ctx context.Context, fn func(*store.Store) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := store.AcquireLock(rt.dbPath)
	if err != nil {
		return err
	}
	defer release()
	st, err := openArchive(ctx, rt.dbPath)
	if err != nil {
		return err
	}
	return errors.Join(fn(st), st.Close())
}

func (rt *runtime) fetchCloud(request sharepointcontent.CanonicalRequest, options browser.LaunchOptions) (CloudFetchReport, error) {
	report := CloudFetchReport{Losses: map[string]int{}}
	canonical, err := sharepointcontent.NormalizeRequest(request.Request)
	if err != nil || canonical != request {
		return report, errs.Usage("invalid canonical cloud request")
	}
	kind := store.CloudPageKind
	if request.Request.Kind == "stream" {
		kind = store.CloudStreamKind
	}
	var attempt store.KnowledgeAttempt
	if err := rt.cloudWrite(rt.ctx, func(st *store.Store) error {
		var err error
		attempt, err = st.BeginCloudAttempt(rt.ctx, store.KnowledgeInput{Kind: kind, Platform: "browser", Locator: request.LocatorKey, At: transcriptsNow()}, request)
		return err
	}); err != nil {
		return report, err
	}
	report.RegistrationID = attempt.LocatorID
	result, err := captureCloud(rt.ctx, options, request)
	completed := transcriptsNow()
	if rt.ctx.Err() != nil {
		result, err = sharepointcontent.Result{}, rt.ctx.Err()
	}
	if err != nil {
		diagnostic := "cloud_unreadable"
		status := "failed"
		var readErr *sharepointcontent.ReadError
		if errors.As(err, &readErr) {
			switch readErr.Code {
			case "signin_required", "identity_mismatch", "too_large", "malformed", "timeout", "no_access", "not_found":
				diagnostic = "cloud_" + readErr.Code
			}
		}
		var coded *errs.Coded
		if errors.As(err, &coded) && coded.Code == errs.CodeBrowserFailed {
			diagnostic = "cloud_browser_failed"
		}
		if rt.ctx.Err() != nil {
			diagnostic, status = "cloud_cancelled", "cancelled"
		}
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(rt.ctx), 10*time.Second)
		defer cancel()
		recordErr := rt.cloudWrite(recordCtx, func(st *store.Store) error {
			return st.RecordCloudFailure(recordCtx, attempt, store.KnowledgeOutcome{Status: status, Reason: diagnostic, At: transcriptsNow()})
		})
		if recordErr != nil {
			return report, errors.Join(err, recordErr)
		}
		if diagnostic == "cloud_signin_required" {
			return report, &errs.Coded{Code: "content_signin_required", Exit: errs.ExitUsage, Message: "content profile is not signed in",
				Fix: "Ask the user to agree, then run `" + rt.contentCommand("signin", request.Request.URL, "--user-agreed") +
					"`; retry the exact stored request with `" + rt.contentCommand("fetch", request.Request.Kind, "--registration-id", fmt.Sprint(attempt.LocatorID)) + "`."}
		}
		return report, err
	}
	err = rt.cloudWrite(rt.ctx, func(st *store.Store) error {
		if err := st.CommitCloudCapture(rt.ctx, attempt, store.CloudCapture{Request: request, Result: result, CompletedAt: completed}); err != nil {
			return err
		}
		snapshot, err := st.CloudSnapshot(rt.ctx, store.CloudFilter{})
		if err != nil {
			return err
		}
		for _, source := range snapshot.Sources {
			if source.Source.AdmittedID != attempt.ID {
				continue
			}
			id := source.Source.ID
			report.SourceID = &id
			report.Scope = source.Source.Coverage
			if source.LastGoodObservationID != nil {
				for _, row := range snapshot.Rows {
					if row.ID == *source.LastGoodObservationID && row.Retained {
						value := fmt.Sprintf("cloud-observation:%d", row.ID)
						report.RetainedObservationID = &value
					}
				}
			}
			for _, row := range snapshot.Rows {
				if row.SourceID == id && !row.Retained {
					value := fmt.Sprintf("cloud-observation:%d", row.ID)
					report.ObservationID = &value
				}
			}
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	report.State, report.Partial = result.State, report.Scope != "selected_transcript"
	for _, loss := range result.Losses {
		report.Losses[loss.Code] += loss.Count
	}
	return report, nil
}

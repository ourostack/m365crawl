package syncer

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

var workIQNow = time.Now
var workIQOpen = workiq.Open
var workIQClose = (*workiq.Client).Close
var workIQWait = waitWorkIQ
var workIQRead = readWorkIQArchive
var workIQWrite = writeWorkIQArchive

func waitWorkIQ(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func workIQFailure(code string) error {
	exit := errs.ExitRuntime
	if code == "workiq_disabled" {
		exit = errs.ExitEnvironment
	}
	return &errs.Coded{Code: code, Exit: exit,
		Message: "the explicit WorkIQ source could not complete (" + code + ")",
		Fix:     "Inspect the optional source setup and retry explicitly; no sign-in or agreement was performed."}
}

func readWorkIQArchive(ctx context.Context, path string, operation func(*store.Store) error) (err error) {
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	return operation(s)
}

func writeWorkIQArchive(ctx context.Context, path string, operation func(context.Context, *store.Store) error) error {
	deadline := workIQNow().Add(10 * time.Minute)
	for {
		err := store.WithWorkIQWriter(ctx, path, operation)
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Code != "locked" {
			return err
		}
		if !workIQNow().Before(deadline) {
			return err
		}
		if err := workIQWait(ctx, min(100*time.Millisecond, deadline.Sub(workIQNow()))); err != nil {
			return err
		}
	}
}

func workIQUser(ctx context.Context, client *workiq.Client) (workiq.User, error) {
	page, err := client.Fetch(ctx, "/me")
	if err != nil {
		return workiq.User{}, err
	}
	return workiq.DecodeUser(ctx, page.Data)
}

func workIQBootstrap(ctx context.Context, options WorkIQOptions) (workiq.User, error) {
	client, err := workIQOpen(ctx, options.Invocation, "")
	if err != nil {
		return workiq.User{}, err
	}
	user, err := workIQUser(ctx, client)
	closeErr := workIQClose(client)
	if err != nil || closeErr != nil {
		return workiq.User{}, errors.Join(err, closeErr)
	}
	return user, nil
}

func workIQPinned(ctx context.Context, options WorkIQOptions, binding workiq.AccountBinding) (*workiq.Client, workiq.User, error) {
	client, err := workIQOpen(ctx, options.Invocation, binding.Address)
	if err != nil {
		return nil, workiq.User{}, err
	}
	user, err := workIQUser(ctx, client)
	if err == nil {
		err = binding.Verify(user)
	}
	if err != nil {
		return nil, workiq.User{}, errors.Join(err, workIQClose(client))
	}
	return client, user, nil
}

type workIQSession struct {
	options WorkIQOptions
	binding workiq.AccountBinding
	client  *workiq.Client
}

func (s *workIQSession) close() error {
	if s.client == nil {
		return nil
	}
	err := workIQClose(s.client)
	s.client = nil
	return err
}

// Parsing HTTP-date metadata does no HTTP I/O. The current documented fetch
// envelope exposes no Retry-After header; an unobservable 429 fails closed
// rather than claiming an absent header or retrying before an unknown deadline.
func workIQRetryDelay(status int, header string, retry int, now time.Time, remaining time.Duration) (time.Duration, bool, error) {
	if status != 429 && (status < 500 || status > 599) || retry >= 3 {
		return 0, false, nil
	}
	if status == 429 && header == "" {
		return 0, false, workIQFailure("workiq_retry_later")
	}
	wait := time.Second << uint(retry)
	if header != "" {
		seconds, err := strconv.ParseInt(header, 10, 64)
		if err == nil && seconds >= 0 {
			if seconds > int64((10*time.Minute)/time.Second) {
				return 0, false, workIQFailure("workiq_retry_later")
			}
			wait = time.Duration(seconds) * time.Second
		} else {
			at, err := http.ParseTime(header)
			if err != nil {
				return 0, false, workIQFailure("workiq_retry_later")
			}
			wait = max(at.Sub(now), 0)
		}
	}
	if remaining <= 0 || wait > remaining {
		return 0, false, workIQFailure("workiq_retry_later")
	}
	return wait, true, nil
}

func (s *workIQSession) fetch(ctx context.Context, path string) (workiq.Page, error) {
	deadline := workIQNow().Add(10 * time.Minute)
	for retry := 0; ; retry++ {
		if err := ctx.Err(); err != nil {
			return workiq.Page{}, err
		}
		if s.client == nil {
			client, _, err := workIQPinned(ctx, s.options, s.binding)
			if err != nil {
				return workiq.Page{}, err
			}
			s.client = client
		}
		page, err := s.client.Fetch(ctx, path)
		if err == nil {
			return page, nil
		}
		if closeErr := s.close(); closeErr != nil {
			return workiq.Page{}, errors.Join(err, closeErr)
		}
		now := workIQNow()
		wait, retryable, policyErr := workIQRetryDelay(page.StatusCode, page.RetryAfter, retry, now, deadline.Sub(now))
		if policyErr != nil {
			return workiq.Page{}, policyErr
		}
		if !retryable {
			return page, err
		}
		if err := workIQWait(ctx, wait); err != nil {
			return workiq.Page{}, err
		}
	}
}

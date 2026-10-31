package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
	"strings"
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
	if code == "workiq_disabled" || code == "workiq_mail_account_missing" || code == "workiq_identity_mismatch" {
		exit = errs.ExitEnvironment
	}
	fix := "Inspect the optional source setup and retry explicitly; no sign-in or agreement was performed."
	switch code {
	case "workiq_page_limit":
		fix = "This run reached its page budget. Mail sync keeps its position: run the same workiq sync again to continue. Only a run that ends with status ok has finished."
	case "workiq_retry_later":
		fix = "WorkIQ kept failing temporarily (throttling or a server error) and nothing past the last saved page advanced. Wait a minute, then run the same workiq command again."
	case "workiq_archive_busy":
		fix = "The archive was too busy to save a page in time; nothing past the last saved page advanced. Run the same workiq command again, ideally when the computer is less busy."
	case "workiq_stuck":
		fix = "WorkIQ failed the same way at the same position as the previous run, so running the same command again will not help. Stop and report this failure with its diagnostic; the run's position is kept."
	}
	return &errs.Coded{Code: code, Exit: exit,
		Message: "the explicit WorkIQ source could not complete (" + code + ")", Fix: fix}
}

func readWorkIQArchive(ctx context.Context, path string, operation func(*store.Store) error) (err error) {
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	return operation(s)
}

// workIQPersistBudget bounds saving a run's outcome, long enough for the write budget's retries.
const workIQPersistBudget = 3 * time.Minute

// withWorkIQWriter is the archive's short server writer; a test seam.
var withWorkIQWriter = store.WithWorkIQWriterFor

type workIQPageBytesKey struct{}

// workIQPageWrite marks ctx as staging a page of n bytes, so its write gets a budget sized to it.
func workIQPageWrite(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, workIQPageBytesKey{}, n)
}

func writeWorkIQArchive(ctx context.Context, path string, operation func(context.Context, *store.Store) error) error {
	budget := store.WorkIQWriteBudget
	if n, ok := ctx.Value(workIQPageBytesKey{}).(int); ok {
		budget = workIQPageWriteBudget(n)
	}
	return writeWorkIQArchiveSized(ctx, path, budget, operation)
}

// workIQPageWriteBudget is the time to write one page: ten seconds plus two per MiB, at most a
// minute. Live pages reached 10.6 MB and missed a flat two-second budget under ordinary load.
func workIQPageWriteBudget(bytes int) time.Duration {
	return min(10*time.Second+time.Duration((bytes+1<<20-1)>>20)*2*time.Second, time.Minute)
}

// writeWorkIQArchiveSized retries a contended writer within the page's ten-minute budget and a
// slow write up to twice with a doubled budget. Every write is guarded by its expected revision,
// so a retry cannot apply twice. A write still too slow is workiq_archive_busy: nothing advanced,
// so the same command run again continues.
func writeWorkIQArchiveSized(ctx context.Context, path string, budget time.Duration, operation func(context.Context, *store.Store) error) error {
	deadline := workIQNow().Add(10 * time.Minute)
	slow := 0
	for {
		err := withWorkIQWriter(ctx, path, budget, operation)
		var coded *errs.Coded
		switch {
		case errors.As(err, &coded) && coded.Code == "locked":
			if !workIQNow().Before(deadline) {
				return err
			}
		case ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
			if slow == 2 {
				return errors.Join(workIQFailure("workiq_archive_busy"), err)
			}
			slow++
			budget = min(2*budget, time.Minute)
		default:
			return err
		}
		if err := workIQWait(ctx, min(100*time.Millisecond, deadline.Sub(workIQNow()))); err != nil {
			return err
		}
	}
}

// workIQAccountChoice gives workiq_account_required a fix that lists each archived Outlook account
// as an exact --account value.
func workIQAccountChoice(err error, facts []workiq.AccountFact) error {
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_account_required" {
		return err
	}
	var choices []string
	for _, fact := range facts {
		if fact.Family == "mail" {
			choice := fact.ID
			if strings.ContainsAny(choice, " '\"$`\\") {
				choice = "'" + strings.ReplaceAll(choice, "'", `'\''`) + "'"
			}
			choices = append(choices, "--account "+choice)
		}
	}
	return &errs.Coded{Code: coded.Code, Exit: coded.Exit, Message: coded.Message,
		Fix: "Choose the Outlook account explicitly with " + strings.Join(choices, " or ") +
			"; WorkIQ then binds that profile to its signed-in user. No profile has a stored WorkIQ identity yet, or several match."}
}

// workIQOutcomeCode is the code a failed run stores, the same one the command prints for err.
func workIQOutcomeCode(err error) string {
	var coded *errs.Coded
	switch {
	case errors.Is(err, context.Canceled):
		return errs.CodeInterrupted
	case errors.As(err, &coded):
		return coded.Code
	}
	return errs.CodeDBError
}

func workIQBootstrap(ctx context.Context, options WorkIQOptions) (workiq.User, error) {
	address := ""
	expectedUser := ""
	if options.Account != "" {
		var facts []workiq.AccountFact
		if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
			facts, err = s.WorkIQAccounts(ctx)
			return err
		}); err != nil {
			return workiq.User{}, err
		}
		known := false
		for _, fact := range facts {
			if fact.ID == options.Account && fact.Family == options.Source {
				known = true
				if fact.Family == "chats" {
					expectedUser = fact.UserID
				}
				var err error
				address, err = fact.PinAddress()
				if err != nil {
					return workiq.User{}, err
				}
				break
			}
		}
		if !known {
			code := "workiq_chat_account_unmatched"
			if options.Source == "mail" {
				code = "workiq_mail_account_missing"
			}
			return workiq.User{}, workIQFailure(code)
		}
	}
	client, user, err := workIQOpenUser(ctx, options.Invocation, address)
	if err != nil {
		return workiq.User{}, err
	}
	if address != "" {
		owner := expectedUser
		if owner == "" {
			owner = user.ID
		}
		err = (workiq.AccountBinding{UserID: owner, Address: address}).Verify(user)
	} else if expectedUser != "" && user.ID != expectedUser {
		err = workIQFailure("workiq_identity_mismatch")
	}
	closeErr := workIQClose(client)
	if err != nil || closeErr != nil {
		return workiq.User{}, errors.Join(err, closeErr)
	}
	return user, nil
}

func workIQPinned(ctx context.Context, options WorkIQOptions, binding workiq.AccountBinding) (*workiq.Client, workiq.User, error) {
	client, user, err := workIQOpenUser(ctx, options.Invocation, binding.Address)
	if err != nil {
		return nil, workiq.User{}, err
	}
	if err := binding.Verify(user); err != nil {
		return nil, workiq.User{}, errors.Join(err, workIQClose(client))
	}
	return client, user, nil
}

// workIQStop marks a run stopped by an unexplained failure that outlasted its retries with where
// (a hash of the request path) and why it stopped, so the next run can tell it stopped at the same
// place again.
type workIQStop struct {
	error
	at string
}

func (e *workIQStop) Unwrap() error { return e.error }

// workIQIdentityStop is an identity check stopped by an unexplained failure. It is never a stuck
// candidate on its own: at startup it means WorkIQ is unavailable. When it ends a reopen during a
// traversal, the stop is marked at the page in progress.
type workIQIdentityStop struct {
	error
	reason string
}

func (e *workIQIdentityStop) Unwrap() error { return e.error }

// workIQStopParts are the parts of a server descriptor that say why a request failed without
// naming the request: its status, its Graph or JSON-RPC code and its kind. Request IDs and other
// text vary per request, so they are not compared.
var workIQStopParts = regexp.MustCompile(`^(?:status [1-5][0-9]{2}|code -?[A-Za-z0-9]{1,64}|tool error|rpc error)$`)

// workIQStopReason is why a failure stopped a run, in terms that repeat when the failure repeats.
func workIQStopReason(cause error) string {
	reason := workIQOutcomeCode(cause)
	d, _ := workiq.DiagnosticOf(cause)
	for _, part := range strings.Split(d.Server, "; ") {
		if part, _, _ = strings.Cut(part, ":"); workIQStopParts.MatchString(part) {
			reason += " " + part
		}
	}
	return reason
}

// workIQStopMark says where (a hash of the request path) and why a run stopped.
func workIQStopMark(path, reason string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8]) + " " + reason
}

// workIQStopped keeps the failure's diagnostic with the retry policy's stop, and marks where an
// unexplained (transient) failure stopped a page. A throttle or server error the server explained
// is not marked: waiting can clear it.
func workIQStopped(transient bool, path string, policyErr, cause error) error {
	err := errors.Join(policyErr, cause)
	if !transient {
		return err
	}
	return &workIQStop{error: err, at: workIQStopMark(path, workIQStopReason(cause))}
}

// workIQStopAt is where and why an error's unexplained failure stopped the run, or "".
func workIQStopAt(err error) string {
	var stop *workIQStop
	if errors.As(err, &stop) {
		return stop.at
	}
	return ""
}

// workIQStuck reports a sync stop as workiq_stuck when the scope's previous sync stopped at the same
// position for the same reason: running again would only repeat it. When the archive cannot say,
// the stop stays the ordinary resumable one.
func workIQStuck(ctx context.Context, options WorkIQOptions, family, account string, cause error) error {
	at := workIQStopAt(cause)
	if at == "" || options.Kind != "sync" {
		return cause
	}
	var last string
	ctx = context.WithoutCancel(ctx)
	if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
		last, err = s.LastWorkIQStop(ctx, family, account, "sync")
		return err
	}); err != nil || last != at {
		return cause
	}
	return errors.Join(workIQFailure("workiq_stuck"), cause)
}

// workIQTransient says a failure is one to retry on a new session: the child ended after the
// session began, or a failure with no status that WorkIQ did not explain.
func workIQTransient(err error, status int) bool {
	code := workIQOutcomeCode(err)
	return code == "workiq_session_lost" || code == "workiq_failed" && status == 0
}

// workIQOpenUser opens a session and reads its signed-in user. A session lost during that identity
// check, or a throttled or failed /me, is reopened under the same retry budget as a page.
func workIQOpenUser(ctx context.Context, inv workiq.Invocation, address string) (*workiq.Client, workiq.User, error) {
	deadline := workIQNow().Add(10 * time.Minute)
	for retry := 0; ; retry++ {
		client, err := workIQOpen(ctx, inv, address)
		if err != nil {
			return nil, workiq.User{}, err
		}
		page, err := client.Fetch(ctx, "/me")
		if err == nil {
			var user workiq.User
			if user, err = workiq.DecodeUser(ctx, page.Data); err == nil {
				return client, user, nil
			}
		}
		closeErr := workIQClose(client)
		transient := workIQTransient(err, page.StatusCode)
		var coded *errs.Coded
		if closeErr != nil && (!transient || errors.As(closeErr, &coded) && coded.Code == "workiq_cleanup_failed") {
			return nil, workiq.User{}, errors.Join(err, closeErr)
		}
		status := page.StatusCode
		if transient {
			status = 503
		}
		now := workIQNow()
		wait, retryable, policyErr := workIQRetryDelay(status, page.RetryAfter, retry, now, deadline.Sub(now))
		if policyErr != nil && transient {
			return nil, workiq.User{}, &workIQIdentityStop{error: errors.Join(policyErr, err), reason: workIQStopReason(err)}
		}
		if policyErr != nil {
			return nil, workiq.User{}, errors.Join(policyErr, err)
		}
		if !retryable {
			return nil, workiq.User{}, err
		}
		if err := workIQWait(ctx, wait); err != nil {
			return nil, workiq.User{}, err
		}
	}
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
	if status != 429 && (status < 500 || status > 599) {
		return 0, false, nil
	}
	if retry >= 3 {
		// Transient failures outlasted the retries; the cursor is unchanged, so rerun later.
		return 0, false, workIQFailure("workiq_retry_later")
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
			var lost *workIQIdentityStop
			if errors.As(err, &lost) { // the reopen of a lost page kept failing: the page is where it stopped
				return workiq.Page{}, &workIQStop{error: err, at: workIQStopMark(path, lost.reason)}
			}
			if err != nil {
				return workiq.Page{}, err
			}
			s.client = client
		}
		page, err := s.client.Fetch(ctx, path)
		if err == nil {
			return page, nil
		}
		// A child that ended after the session began, or a failure with no status that WorkIQ did not
		// explain, is reopened and the same page retried like a server error; only a cleanup failure
		// stops the run at once.
		transient := workIQTransient(err, page.StatusCode)
		var coded *errs.Coded
		if closeErr := s.close(); closeErr != nil && (!transient || errors.As(closeErr, &coded) && coded.Code == "workiq_cleanup_failed") {
			return workiq.Page{}, errors.Join(err, closeErr)
		}
		status := page.StatusCode
		if transient {
			status = 503
		}
		now := workIQNow()
		wait, retryable, policyErr := workIQRetryDelay(status, page.RetryAfter, retry, now, deadline.Sub(now))
		if policyErr != nil {
			return workiq.Page{}, workIQStopped(transient, path, policyErr, err)
		}
		if !retryable {
			return page, err
		}
		if err := workIQWait(ctx, wait); err != nil {
			return workiq.Page{}, err
		}
	}
}

// workIQCloseStale settles the open attempts of one scope left by runs whose completion was never
// saved; a test seam.
var workIQCloseStale = func(ctx context.Context, path string, scope store.WorkIQScope, at time.Time) error {
	return writeWorkIQArchive(ctx, path, func(writeCtx context.Context, s *store.Store) error {
		_, err := s.CloseStaleWorkIQAttempts(writeCtx, scope, at)
		return err
	})
}

// workIQRunLockPath names the run lock of one (family, account, kind) scope on an archive. The
// scope is encoded unambiguously before hashing, so no two scopes share a lock.
func workIQRunLockPath(db, family, account, kind string) string {
	key, _ := json.Marshal([]string{family, account, kind})
	sum := sha256.Sum256(key)
	return db + ".workiq-run-" + hex.EncodeToString(sum[:16])
}

// workIQRunLocks holds the scope locks one command owns until it ends.
type workIQRunLocks struct {
	mu       sync.Mutex
	releases map[string]func()
}

func (l *workIQRunLocks) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for path, release := range l.releases {
		release()
		delete(l.releases, path)
	}
}

// holdWorkIQScope owns one (family, account, kind) scope on this archive until the command ends,
// then closes that scope's stale attempts. A scope this command already owns, as on an expired
// cursor or recheck restart, is not taken twice. Runs of other scopes proceed concurrently.
func holdWorkIQScope(ctx context.Context, options WorkIQOptions, scope store.WorkIQScope) error {
	locks := options.runLocks
	if locks == nil {
		return errors.New("WorkIQ scope lock requires a command lock holder")
	}
	if scope.Kind == "" {
		scope.Kind = "sync"
	}
	path := workIQRunLockPath(options.DBPath, scope.Family, scope.Account, scope.Kind)
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if locks.releases[path] != nil {
		return nil
	}
	release, err := store.AcquireLock(path)
	if err != nil {
		return err
	}
	if err := workIQCloseStale(ctx, options.DBPath, scope, workIQNow()); err != nil {
		release()
		return err
	}
	if locks.releases == nil {
		locks.releases = map[string]func(){}
	}
	locks.releases[path] = release
	return nil
}

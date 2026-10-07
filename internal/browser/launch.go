package browser

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

// LaunchFlags is the whole browser command line apart from the profile, the optional headless
// flag and the start page. Pipe mode (--remote-debugging-pipe) was considered and rejected:
// Go's ExtraFiles is not supported on Windows, so the debugging port is used on every platform
// and is exposed on loopback for the length of one run.
//
// The first dogfooded set was only --headless=new, a fresh --user-data-dir and
// --remote-debugging-port=0. If silent sign-in fails with this set, drop the added flags first.
const LaunchFlags = "--remote-debugging-port=0 --no-first-run --no-default-browser-check --disable-background-networking --disable-sync --disable-crash-reporter"

// DefaultLaunchTimeout is how long Launch waits for the browser to publish its debugging port.
const DefaultLaunchTimeout = 20 * time.Second

// LaunchOptions says how to start the browser.
type LaunchOptions struct {
	Exe      string
	Kind     Kind
	Profile  string
	Headless bool
	StartURL string // the page the browser opens; empty means about:blank
	Timeout  time.Duration
}

// Argv is the exact command line (without the executable) Launch uses.
func Argv(o LaunchOptions) []string {
	args := []string{"--user-data-dir=" + o.Profile}
	args = append(args, strings.Fields(LaunchFlags)...)
	if o.Headless {
		args = append(args, "--headless=new")
	}
	start := o.StartURL
	if start == "" {
		start = "about:blank"
	}
	return append(args, start)
}

// ProfileDir is the browser profile that belongs to an archive.
func ProfileDir(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), "browser") }

// EnsureProfileDir creates the profile directory, readable by its owner only. Under the archive
// directory it also inherits that directory's private access rules on Windows.
func EnsureProfileDir(profile string) error {
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	return os.Chmod(profile, 0o700) //nolint:gosec // G302: a directory, 0700 is the point
}

// Test seams.
var (
	acquireLock     = store.AcquireLock
	adoptProc       = adopt
	singletonLiveFn = singletonLive
	sweepOrphan     = SweepOrphan
	dialFn          = dialCDP
	pollEvery       = 25 * time.Millisecond
	closeWait       = 2 * time.Second
)

// Browser is a running browser that this process owns.
type Browser struct {
	Kind Kind

	profile string
	cmd     *exec.Cmd
	group   *group
	exited  chan struct{}
	status  string // how the leader ended, once exited is closed
	release func()
	wsURL   string

	mu        sync.Mutex // guards conn and page, never held across a network call
	pageMu    sync.Mutex // serializes Page()
	conn      *client
	page      *Page
	closeOnce sync.Once
	closeErr  error
}

// Launch starts the browser. It takes the profile lock, sweeps any orphan, refuses a profile a
// live browser holds, starts the browser in its own process group (job object on Windows),
// and waits for the debugging port. On any failure it leaves no process behind.
func Launch(ctx context.Context, o LaunchOptions) (*Browser, error) {
	release, err := acquireLock(o.Profile)
	if err != nil {
		var coded *errs.Coded
		if errors.As(err, &coded) && coded.Code == errs.CodeLocked {
			return nil, errs.BrowserBusy()
		}
		return nil, err
	}
	b, err := launchLocked(ctx, o, release)
	if err != nil {
		release()
		return nil, err
	}
	return b, nil
}

func launchLocked(ctx context.Context, o LaunchOptions, release func()) (*Browser, error) {
	if err := EnsureProfileDir(o.Profile); err != nil {
		return nil, errs.BrowserFailed("could not prepare the browser profile: " + err.Error())
	}
	if err := sweepOrphan(o.Profile); err != nil {
		return nil, errs.BrowserFailed("an earlier browser would not stop: " + err.Error())
	}
	if singletonLiveFn(o.Profile) {
		return nil, errs.BrowserBusy()
	}
	_ = os.Remove(filepath.Join(o.Profile, devToolsFile))

	cmd := exec.Command(o.Exe, Argv(o)...) //nolint:gosec // G204: the executable is the browser the user chose; stdio stays on the null device
	prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		return nil, errs.BrowserFailed("could not start the browser: " + err.Error())
	}
	b := &Browser{Kind: o.Kind, profile: o.Profile, cmd: cmd, exited: make(chan struct{}), release: release}
	go func() {
		err := cmd.Wait()
		b.status = exitStatus(err)
		close(b.exited)
	}()
	g, err := adoptProc(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		<-b.exited
		_ = sweepArgv(o.Profile, true)
		return nil, errs.BrowserFailed("could not contain the browser process: " + err.Error())
	}
	b.group = g
	register(b)
	fail := func(err error) (*Browser, error) {
		_ = b.stop()
		return nil, err
	}
	if err := writePidFile(o.Profile, cmd.Process.Pid, processStart(cmd)); err != nil {
		return fail(errs.BrowserFailed("could not record the browser process: " + err.Error()))
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultLaunchTimeout
	}
	wsURL, err := b.waitForPort(ctx, timeout)
	if err != nil {
		return fail(err)
	}
	b.wsURL = wsURL
	return b, nil
}

func exitStatus(err error) string {
	if err == nil {
		return "0"
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return strconv.Itoa(ee.ExitCode())
	}
	return err.Error()
}

// waitForPort polls DevToolsActivePort until the browser publishes its debugging address.
func (b *Browser) waitForPort(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		if u, ok := readDevToolsPort(b.profile); ok {
			return u, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errs.BrowserFailed("did not start")
		case <-b.exited:
			// The launch may have handed off to a browser that already ran on this profile.
			if u, ok := readDevToolsPort(b.profile); ok {
				return u, nil
			}
			if singletonLiveFn(b.profile) {
				return "", errs.BrowserBusy()
			}
			return "", errs.BrowserFailed("the browser exited with status " + b.status + " before it opened a debugging port")
		case <-tick.C:
		}
	}
}

// readDevToolsPort parses DevToolsActivePort: a port, then the browser endpoint path.
func readDevToolsPort(profile string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(profile, devToolsFile)) //nolint:gosec // G304: inside the profile this run owns
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		return "", false
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	path := strings.TrimSpace(lines[1])
	if err != nil || port < 1 || port > 65535 || !strings.HasPrefix(path, "/devtools/browser/") {
		return "", false
	}
	return "ws://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + path, true
}

// connect dials the browser endpoint once and keeps the connection. The mutex guards only the
// field, never a network call, so Close can always get in.
func (b *Browser) connect(ctx context.Context) (*client, error) {
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c != nil {
		return c, nil
	}
	c, err := dialFn(ctx, b.wsURL)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil { // another caller connected first
		c.close()
		return b.conn, nil
	}
	b.conn = c
	return c, nil
}

// Close ends the browser and everything it started, whatever state it is in: Browser.close
// first, then the process group (SIGTERM, then SIGKILL), then any leftover process that runs
// with this profile. It removes the pid file and releases the profile lock. It is idempotent
// and returns an error only when a process survived.
func (b *Browser) Close() error {
	b.closeOnce.Do(func() {
		b.askToClose()
		b.closeErr = b.stop()
	})
	return b.closeErr
}

// askToClose sends Browser.close and gives the leader a moment to exit by itself.
func (b *Browser) askToClose() {
	ctx, cancel := context.WithTimeout(context.Background(), closeWait)
	defer cancel()
	c, err := b.connect(ctx)
	if err == nil {
		_ = c.call(ctx, "", "Browser.close", nil, nil)
	}
	select {
	case <-b.exited:
	case <-ctx.Done():
	}
}

// stop is the hard part of Close: it ends the group and every leftover, then lets go.
func (b *Browser) stop() error {
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c != nil {
		c.close() // ends any call still waiting on a browser that stopped answering
	}
	if b.worthSignallingGroup() {
		stopGroup(b.group)
	}
	err := sweepArgv(b.profile, true)
	b.group.release()
	_ = os.Remove(pidPath(b.profile))
	unregister(b)
	b.release()
	return err
}

// worthSignallingGroup says whether to signal the process group. Once the leader has exited and
// nothing runs with the profile, the group id may already belong to someone else, so the
// signal is skipped.
func (b *Browser) worthSignallingGroup() bool {
	select {
	case <-b.exited:
	default:
		return true
	}
	return groupHasProfileProcs(b.profile)
}

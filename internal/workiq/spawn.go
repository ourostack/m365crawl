package workiq

import (
	"context"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	callTimeout = 120 * time.Second
	maxFrame    = 16 << 20
	maxPath     = 64 << 10
	maxStderr   = 64 << 10
)

var (
	buildCommand   = commandFor
	adoptChild     = adoptProcess
	requestTimeout = callTimeout
	// A signed-in WorkIQ 1.0.1 answered initialize within 1.6-3.5 s; one with no cached account
	// stays silent while it opens its own interactive sign-in.
	handshakeTimeout = 30 * time.Second
	readVersion      = io.ReadAll
)

type mode uint8

const (
	readMCP mode = iota
	versionOnly
)

type diagnosticBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if left := maxStderr - len(b.b); left > 0 {
		b.b = append(b.b, p[:min(left, len(p))]...)
	}
	return len(p), nil
}

// empty says the child wrote nothing to stderr.
func (b *diagnosticBuffer) empty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.b) == 0
}

func (b *diagnosticBuffer) diagnostic() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return classify(b.b)
}

type process struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output io.ReadCloser
	errors diagnosticBuffer
	group  *processGroup
	once   sync.Once
	stop   chan struct{}
	err    error
}

func validAccount(s string) bool {
	if s == "" {
		return true // explicit /me bootstrap has no caller-supplied address
	}
	if len(s) > 254 || strings.Count(s, "@") != 1 {
		return false
	}
	local, domain, _ := strings.Cut(s, "@")
	if local == "" || len(local) > 64 || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return false
	}
	for _, c := range local {
		allowed := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("._+-", c)
		if !allowed {
			return false
		}
	}
	if domain == "" {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			allowed := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-'
			if !allowed {
				return false
			}
		}
	}
	return true
}

// startWorkIQ is the only subprocess-start boundary. No caller can supply a verb or argv.
func chooseBinary(inv Invocation, underTest bool) (string, error) {
	binary := os.Getenv("M365CRAWL_WORKIQ_BIN")
	if binary == "" && (underTest || inv.ExplicitTeamsRoot || inv.ExplicitOutlookRoot) {
		return "", failure("workiq_hermetic_required")
	}
	if binary != "" {
		return binary, nil
	}
	path, err := exec.LookPath("workiq")
	if err != nil {
		return "", failure("workiq_not_installed")
	}
	return path, nil
}

func startWorkIQ(ctx context.Context, inv Invocation, operation mode, account string) (*process, error) {
	binary, err := chooseBinary(inv, testing.Testing())
	if err != nil {
		return nil, err
	}
	if !validAccount(account) {
		return nil, failure("workiq_account_invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, failure("workiq_timeout")
	}
	args := []string{"mcp"}
	switch operation {
	case versionOnly:
		args = []string{"version"}
	case readMCP:
		if account != "" {
			args = append(args, "--account", account)
		}
	default:
		return nil, failure("workiq_protocol")
	}
	cmd, err := buildCommand(binary, args)
	if err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, stop: make(chan struct{})}
	p.cmd.Stderr = &p.errors
	p.cmd.WaitDelay = 5 * time.Second
	if p.input, err = cmd.StdinPipe(); err != nil {
		return nil, failure("workiq_failed")
	}
	if p.output, err = cmd.StdoutPipe(); err != nil {
		_ = p.input.Close()
		return nil, failure("workiq_failed")
	}
	prepareProcess(cmd)
	if err = cmd.Start(); err != nil {
		_ = p.input.Close()
		_ = p.output.Close()
		return nil, failure("workiq_not_installed")
	}
	p.group, err = adoptChild(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = p.close()
		return nil, failure("workiq_cleanup_failed")
	}
	return p, nil
}

func (p *process) close() error {
	p.once.Do(func() {
		close(p.stop)
		_ = p.input.Close()
		exited := make(chan error, 1)
		go func() { exited <- p.cmd.Wait() }()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case err := <-exited:
			if err != nil {
				p.err = p.errors.diagnostic()
			}
		case <-timer.C:
			if p.group != nil {
				p.err = p.group.end()
			}
			if err := <-exited; err != nil && p.err == nil {
				p.err = p.errors.diagnostic()
			}
		}
		_ = p.output.Close()
		if p.group != nil {
			if err := p.group.end(); err != nil {
				p.err = failure("workiq_cleanup_failed")
			}
		}
	})
	return p.err
}

// Version accepts only a numeric version token, never arbitrary child stdout.
func Version(ctx context.Context, inv Invocation) (string, error) {
	p, err := startWorkIQ(ctx, inv, versionOnly, "")
	if err != nil {
		return "", err
	}
	_ = p.input.Close()
	type result struct {
		raw []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		b, err := readVersion(io.LimitReader(p.output, maxStderr+1))
		done <- result{b, err}
	}()
	deadline, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	select {
	case r := <-done:
		closeErr := p.close()
		if r.err != nil || len(r.raw) > maxStderr {
			return "", failure("workiq_protocol")
		}
		if closeErr != nil {
			return "", closeErr
		}
		if match := regexp.MustCompile(`(?m)^(?:WorkIQ(?: CLI)? )?v?([0-9]+\.[0-9]+\.[0-9]+)(?:\.[0-9]+)?(?:\+[0-9A-Za-z.-]+)?\r?$`).FindSubmatch(r.raw); len(match) > 0 {
			return string(match[1]), nil
		}
		return "", classify(r.raw)
	case <-deadline.Done():
		_ = p.close()
		<-done
		return "", failure("workiq_timeout")
	}
}

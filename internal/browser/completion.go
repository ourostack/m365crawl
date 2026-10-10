package browser

import "time"

type completionFailure struct {
	code  string
	cause error
}

func completionBufferFits(capacity, returned, header, offset, length uint32) bool {
	return returned <= capacity && returned >= header && offset >= header && offset <= returned && length <= returned-offset
}

func (e *completionFailure) Error() string { return e.code }
func (e *completionFailure) Unwrap() error { return e.cause }

type completionIdentity struct {
	pid     uint32
	started int64
}

type completionTarget struct {
	identity  completionIdentity
	inJob     bool
	poll      func() (bool, error)
	terminate func() error
	release   func() error
}

type completionSet struct {
	targets  []*completionTarget
	deadline time.Time
	limit    int
}

func (s *completionSet) add(t *completionTarget) error {
	code := ""
	switch {
	case t == nil:
		return &completionFailure{code: "browser_completion_identity_failed"}
	case t.identity.pid == 0 || t.identity.started == 0 || t.poll == nil || t.terminate == nil || t.release == nil:
		code = "browser_completion_identity_failed"
	case !time.Now().Before(s.deadline):
		code = "browser_completion_timeout"
	default:
		for _, held := range s.targets {
			if held.identity == t.identity {
				held.inJob = held.inJob || t.inJob
				if err := t.release(); err != nil {
					return &completionFailure{code: "browser_completion_release_failed", cause: err}
				}
				return nil
			}
		}
		if len(s.targets) >= s.limit {
			code = "browser_completion_too_large"
		} else {
			s.targets = append(s.targets, t)
			return nil
		}
	}
	var err error
	if t.release != nil {
		err = t.release()
	}
	return &completionFailure{code: code, cause: err}
}

func (s *completionSet) poll() (bool, error) {
	done := true
	for _, t := range s.targets {
		if !time.Now().Before(s.deadline) {
			return false, &completionFailure{code: "browser_completion_timeout"}
		}
		signalled, err := t.poll()
		if err != nil {
			return false, &completionFailure{code: "browser_completion_wait_failed", cause: err}
		}
		done = done && signalled
	}
	return done, nil
}

func (s *completionSet) terminateFallback() error {
	var first error
	for _, t := range s.targets {
		if t.inJob {
			continue
		}
		if !time.Now().Before(s.deadline) {
			if first == nil {
				first = &completionFailure{code: "browser_completion_timeout"}
			}
			break
		}
		signalled, err := t.poll()
		if err != nil {
			if first == nil {
				first = &completionFailure{code: "browser_completion_wait_failed", cause: err}
			}
			continue
		}
		if signalled {
			continue
		}
		if !time.Now().Before(s.deadline) {
			if first == nil {
				first = &completionFailure{code: "browser_completion_timeout"}
			}
			break
		}
		if err := t.terminate(); err != nil && first == nil {
			first = &completionFailure{code: "browser_completion_terminate_failed", cause: err}
		}
	}
	return first
}

func (s *completionSet) release() error {
	targets := s.targets
	s.targets = nil
	var first error
	for _, t := range targets {
		if err := t.release(); err != nil && first == nil {
			first = &completionFailure{code: "browser_completion_release_failed", cause: err}
		}
	}
	return first
}

func waitCompletion(deadline time.Time, poll func() (bool, error)) error {
	for {
		if !time.Now().Before(deadline) {
			return &completionFailure{code: "browser_completion_timeout"}
		}
		done, err := poll()
		if err != nil {
			return &completionFailure{code: "browser_completion_wait_failed", cause: err}
		}
		if done {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining > waitPollEvery {
			remaining = waitPollEvery
		}
		if remaining > 0 {
			time.Sleep(remaining)
		}
	}
}

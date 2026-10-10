package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrEval is a page script that threw. It carries the exception's class name only: the
// exception text and description can quote page content, which must never reach a log.
type ErrEval struct{ Name string }

func (e *ErrEval) Error() string { return "the page script threw " + e.Name }

var classNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$]{0,63}$`)

// Test seams for the retry loops.
var (
	retryEvery   = 100 * time.Millisecond
	retryLimit   = 50
	loadPollWait = 50 * time.Millisecond
)

// Page is the one tab the browser opened. A Page is not safe for concurrent use by callers that
// need their own ordering, but each call is independent.
type Page struct {
	c       *client
	session string
}

// Page attaches to the initial about:blank tab (or opens one if there is none) and returns it.
// Every later call carries the session id. The same Page comes back on later calls.
func (b *Browser) Page(ctx context.Context) (*Page, error) {
	b.pageMu.Lock()
	defer b.pageMu.Unlock()
	b.mu.Lock()
	existing := b.page
	b.mu.Unlock()
	if existing != nil {
		return existing, nil
	}
	c, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := c.call(ctx, "", "Target.getTargets", nil, &targets); err != nil {
		return nil, err
	}
	id := ""
	for _, t := range targets.TargetInfos {
		if t.Type == "page" {
			id = t.TargetID
			break
		}
	}
	if id == "" {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			return nil, err
		}
		id = created.TargetID
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": id, "flatten": true}, &attached); err != nil {
		return nil, err
	}
	p := &Page{c: c, session: attached.SessionID}
	b.mu.Lock()
	b.page = p
	b.mu.Unlock()
	return p, nil
}

// Navigate loads url in the tab and waits for the document to finish loading.
func (p *Page) Navigate(ctx context.Context, url string) error {
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if err := p.c.call(ctx, p.session, "Page.navigate", map[string]any{"url": url}, &res); err != nil {
		return err
	}
	if res.ErrorText != "" {
		return fmt.Errorf("navigation failed: %s", res.ErrorText)
	}
	for {
		var state string
		err := p.Eval(ctx, "document.readyState", &state)
		if err == nil && state == "complete" {
			return nil
		}
		if err != nil && !isDestroyed(err) {
			return err
		}
		if err := sleep(ctx, loadPollWait); err != nil {
			return err
		}
	}
}

// Host returns location.host of the tab, retrying while a navigation destroys the page's
// execution context.
func (p *Page) Host(ctx context.Context) (string, error) {
	var host string
	var err error
	for i := 0; i < retryLimit; i++ {
		if err = p.Eval(ctx, "location.host", &host); err == nil || !isDestroyed(err) {
			break
		}
		if serr := sleep(ctx, retryEvery); serr != nil {
			return host, serr
		}
	}
	return host, err
}

// Eval runs expr in the page, waits for a returned promise, and decodes the value into out
// (when out is not nil). A script that throws gives *ErrEval.
func (p *Page) Eval(ctx context.Context, expr string, out any) error {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Exception *struct {
				ClassName string `json:"className"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	params := map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true}
	if err := p.c.call(ctx, p.session, "Runtime.evaluate", params, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		name := "Error"
		if ex := res.ExceptionDetails.Exception; ex != nil && classNameRe.MatchString(ex.ClassName) {
			name = ex.ClassName
		}
		return &ErrEval{Name: name}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(res.Result.Value, out)
}

// AddInitScript registers a script before future document application code runs.
// If the registration response is lost, the caller must close its owned browser,
// not retry registration or infer an identifier.
func (p *Page) AddInitScript(ctx context.Context, source string) (string, error) {
	if err := p.c.call(ctx, p.session, "Page.enable", nil, nil); err != nil {
		return "", err
	}
	var result struct {
		Identifier string `json:"identifier"`
	}
	if err := p.c.call(ctx, p.session, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": source}, &result); err != nil {
		return "", err
	}
	if result.Identifier == "" {
		return "", errors.New("browser script registration identifier missing")
	}
	return result.Identifier, nil
}

// RemoveInitScript removes only the returned registration from this page.
func (p *Page) RemoveInitScript(ctx context.Context, identifier string) error {
	if identifier == "" {
		return errors.New("browser script registration identifier missing")
	}
	return p.c.call(ctx, p.session, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{"identifier": identifier}, nil)
}

func isDestroyed(err error) bool {
	var ce *CDPError
	if !errors.As(err, &ce) {
		return false
	}
	return strings.Contains(ce.Message, "Execution context was destroyed") ||
		strings.Contains(ce.Message, "Cannot find context with specified id")
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

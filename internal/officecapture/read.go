package officecapture

import (
	"context"
	"errors"
	"os"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

// ReadJSON binds the parser to the capture's owned inode, never its public Path.
func (c *JSONCapture) ReadJSON(ctx context.Context) (officedocuments.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || ctx.Err() != nil || c.ctx.Err() != nil || c.validateFields() != nil {
		return officedocuments.Result{}, errors.Join(jsonError(c.ctx, "office_json_capture_failed"), ctx.Err())
	}
	input, err := openInput(c.privatePath, false)
	if err != nil {
		return officedocuments.Result{}, jsonError(c.ctx, "office_json_capture_failed")
	}
	info, statErr := input.file().Stat()
	if statErr != nil || !os.SameFile(c.privateInfo, info) {
		return officedocuments.Result{}, errors.Join(jsonError(c.ctx, "office_json_capture_failed"), safeInputClose(c.ctx, input))
	}
	result, readErr := officedocuments.ReadJSON(c.ctx, input.file(), c.surface)
	closeErr := safeInputClose(c.ctx, input)
	if readErr != nil || closeErr != nil || ctx.Err() != nil || c.ctx.Err() != nil {
		return officedocuments.Result{}, errors.Join(jsonError(c.ctx, "office_json_capture_failed"), ctx.Err())
	}
	return result, nil
}

func safeInputClose(ctx context.Context, input *guardedInput) error {
	if err := input.close(); err != nil {
		return jsonError(ctx, "office_json_capture_failed")
	}
	return nil
}

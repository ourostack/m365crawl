//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Operational refusals bound one observation; these are not mailbox-size correctness thresholds. Parent owns the outer process/TEMP lifetime, including forced termination.
const (
	owaWorkTimeout      = 2 * time.Minute
	owaMaxQueryRows     = 16384
	owaMaxQueryBytes    = 512 << 20
	owaMaxStateBytes    = 512 << 20
	owaMaxSourceFiles   = 32768
	owaMaxSourceBytes   = 512 << 20
	owaMaxRawValueBytes = 8 << 20
	owaMaxPayloadBytes  = 8 << 20
	owaMaxDecodedBytes  = 512 << 20
	owaMaxHTMLTokens    = 262144
)

func owaProjectionBudget(rows, bytes, max int64) error {
	if rows < 0 || rows > owaMaxQueryRows || bytes < 0 || max < 0 || max > owaMaxQueryBytes || bytes > max {
		return errors.New("query_projection_budget")
	}
	return nil
}

func owaDecodedBudget(used, next int64) error {
	if next < 0 || used < 0 || next > owaMaxPayloadBytes {
		return errors.New("census_resource_refused")
	}
	if used > owaMaxDecodedBytes || next > owaMaxDecodedBytes-used {
		return errors.New("aggregate_payload_budget")
	}
	return nil
}

func owaQualificationCleanup(t *testing.T, emit func() error) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() && emit() != nil {
			t.Error("qualification_witness_refused")
		}
	})
}

func owaWorkContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), owaWorkTimeout)
	t.Cleanup(cancel)
	return ctx
}

func owaOwnedSnapshot(ctx context.Context, source teamsdesktop.Source, copySnapshot func(context.Context, teamsdesktop.Source) (string, func(), error)) (string, func(), error) {
	ctx, cancelWork := context.WithTimeout(ctx, owaWorkTimeout)
	defer cancelWork()
	if ctx.Err() != nil {
		return "", func() {}, errors.New("work_context_refused")
	}
	copyCtx, cancelCopy := context.WithCancel(context.WithoutCancel(ctx))
	forwarded := make(chan struct{})
	stopForwarding := context.AfterFunc(ctx, func() {
		cancelCopy()
		close(forwarded)
	})
	stop := func() {
		if !stopForwarding() {
			<-forwarded
		}
	}
	dir, cleanup, err := copySnapshot(copyCtx, source)
	stop()
	if err != nil || ctx.Err() != nil {
		cleanup()
		cancelCopy()
		return "", func() {}, errors.New("snapshot_work_refused")
	}
	var once sync.Once
	return dir, func() {
		once.Do(func() {
			cleanup()
			cancelCopy()
		})
	}, nil
}

func owaQueryContext(ctx context.Context, st *store.Store, q string) ([][]any, error) {
	return owaQueryBudget(ctx, st, q, owaMaxQueryBytes)
}

func owaQueryBudget(parent context.Context, st *store.Store, q string, maxBytes int64) ([][]any, error) {
	ctx, cancel := context.WithTimeout(parent, owaWorkTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, errors.New("query_context_refused")
	}
	// The source query and its schema are fixed acceptance-owned SQL. Inspect names with zero rows, then count projected bytes before materializing any payload.
	cols, _, _, err := st.SQL(ctx, "select * from ("+q+") limit 0", 0)
	if err != nil || len(cols) == 0 {
		return nil, errors.New("archive_query_refused")
	}
	lengths := make([]string, len(cols))
	for i, col := range cols {
		lengths[i] = `coalesce(length(cast("` + strings.ReplaceAll(col, `"`, `""`) + `" as blob)),0)`
	}
	preflight := "select count(*),coalesce(sum(" + strings.Join(lengths, "+") + "),0) from (" + q + ")"
	_, counts, truncated, err := st.SQL(ctx, preflight, 1)
	if err != nil || truncated || len(counts) != 1 || len(counts[0]) != 2 {
		return nil, errors.New("archive_query_refused")
	}
	rows, rowOK := counts[0][0].(int64)
	bytes, byteOK := counts[0][1].(int64)
	if !rowOK || !byteOK {
		return nil, errors.New("query_resource_refused")
	}
	if err := owaProjectionBudget(rows, bytes, maxBytes); err != nil {
		return nil, err
	}
	_, result, truncated, err := st.SQL(ctx, q, owaMaxQueryRows)
	if err != nil || truncated || ctx.Err() != nil {
		return nil, errors.New("archive_query_refused")
	}
	return result, nil
}

func owaCheckSource(ctx context.Context, source teamsdesktop.Source) error {
	if ctx.Err() != nil {
		return errors.New("source_context_refused")
	}
	var files, bytes int64
	for _, dir := range []string{source.LevelDBDir, source.BlobDir} {
		if dir == "" {
			continue
		}
		err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return errors.New("source_context_refused")
			}
			if err != nil {
				return errors.New("source_inventory_refused")
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("source_inventory_refused")
			}
			files++
			if info.Size() < 0 || files > owaMaxSourceFiles || info.Size() > owaMaxSourceBytes-bytes || dir == source.BlobDir && info.Size() > owaMaxPayloadBytes {
				return errors.New("source_resource_refused")
			}
			bytes += info.Size()
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Cancellation is checked between bounded file-read chunks; no raw IO error crosses the acceptance boundary.
type owaContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r owaContextReader) Read(p []byte) (int, error) {
	if r.ctx.Err() != nil {
		return 0, errors.New("payload_context_refused")
	}
	return r.reader.Read(p)
}

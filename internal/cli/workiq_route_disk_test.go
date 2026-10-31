package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func setRouteDisk(t *testing.T, estimate func(*store.Store, context.Context, string) (store.WorkIQMailDisk, bool, error), free func(string) (int64, error)) {
	t.Helper()
	oldEstimate, oldFree := workIQMailDiskEstimate, workIQRouteFreeBytes
	t.Cleanup(func() { workIQMailDiskEstimate, workIQRouteFreeBytes = oldEstimate, oldFree })
	workIQMailDiskEstimate, workIQRouteFreeBytes = estimate, free
}

func routeStepDoes(t *testing.T, route map[string]any, command string) string {
	t.Helper()
	for _, step := range route["next"].([]any) {
		s := step.(map[string]any)
		if strings.Contains(s["command"].(string), command) {
			return s["does"].(string)
		}
	}
	return ""
}

// A thread route ends in whole-account sync: it states what that costs, and says so plainly when it
// cannot fit here.
func TestOlderMailRouteStatesWholeAccountSyncCost(t *testing.T) {
	e := textEnv(t)
	seedMail(t, e, seedMessages()[1:]) // the plan that the first reply answers is not archived
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "absent"))
	huge := store.WorkIQMailDisk{InventoryItems: 366000, FetchedItems: 15502, StagedItems: 15502, StagedBytes: 877 << 20, LargestPage: 10 << 20}
	read := func() map[string]any {
		t.Helper()
		routes := routesOf(t, mailJSON(t, e, "mail", "thread", "outlook/Main:102"))
		if len(routes) != 1 {
			t.Fatalf("want one route, got %v", routes)
		}
		return routes[0]
	}

	setRouteDisk(t, func(*store.Store, context.Context, string) (store.WorkIQMailDisk, bool, error) {
		return store.WorkIQMailDisk{}, false, nil
	},
		func(string) (int64, error) { return 1 << 40, nil })
	if does := routeStepDoes(t, read(), "workiq sync"); !strings.Contains(does, "unknown until WorkIQ lists the folders") || !strings.Contains(does, "340 KB") ||
		!strings.Contains(does, "resumable: true") {
		t.Fatalf("unknown cost was not stated honestly: %q", does)
	}

	setRouteDisk(t, func(_ *store.Store, _ context.Context, account string) (store.WorkIQMailDisk, bool, error) {
		return store.WorkIQMailDisk{InventoryItems: 2000}, account == mailAccount, nil
	}, func(string) (int64, error) { return 1 << 40, nil })
	if does := routeStepDoes(t, read(), "workiq sync"); !strings.Contains(does, "about 2000 more items in about 200 pages") || !strings.Contains(does, "GB") {
		t.Fatalf("a known cost was not stated: %q", does)
	}

	setRouteDisk(t, func(*store.Store, context.Context, string) (store.WorkIQMailDisk, bool, error) {
		return huge, true, nil
	},
		func(string) (int64, error) { return 15 << 30, nil })
	r := read()
	fix, _ := r["fix"].(string)
	cmds := routeCommands(t, r)
	if !strings.Contains(fix, "is refused on this archive") || !strings.Contains(fix, "--since and --until") ||
		!strings.Contains(fix, "350498 more items") || len(cmds) != 2 || strings.Contains(strings.Join(cmds, " "), "workiq sync") {
		t.Fatalf("a sync that cannot fit was still offered: %q %q", fix, cmds)
	}
	// The guidance matches what is left: free the shortfall or wait for range backfill; the remaining
	// steps only check and enable WorkIQ, and repeating the read cannot change the result yet.
	if strings.Contains(fix, "run these steps, then repeat the read") || !strings.Contains(fix, "Free at least 108.8 GB") ||
		!strings.Contains(fix, "the steps below only check WorkIQ and turn it on") {
		t.Fatalf("refused guidance does not match its steps: %q", fix)
	}

	setRouteDisk(t, func(*store.Store, context.Context, string) (store.WorkIQMailDisk, bool, error) {
		return huge, true, nil
	},
		func(string) (int64, error) { return 0, errors.New("synthetic free-space failure") })
	if does := routeStepDoes(t, read(), "workiq sync"); !strings.Contains(does, "unknown until WorkIQ lists the folders") {
		t.Fatalf("unknown free space claimed a cost: %q", does)
	}

	setRouteDisk(t, func(*store.Store, context.Context, string) (store.WorkIQMailDisk, bool, error) {
		return store.WorkIQMailDisk{}, false, errors.New("broken")
	}, func(string) (int64, error) { return 1 << 40, nil })
	mailFails(t, e, "db_error", 1, "mail", "thread", "outlook/Main:102")
}

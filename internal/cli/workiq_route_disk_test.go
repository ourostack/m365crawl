package cli

import (
	"context"
	"errors"
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

// The route states what whole-account sync costs, and says so plainly when it cannot fit here,
// without promising anything beyond the follow-up issue.
func TestOlderMailRouteStatesWholeAccountSyncCost(t *testing.T) {
	e := routeEnv(t)
	huge := store.WorkIQMailDisk{InventoryItems: 366000, FetchedItems: 15502, StagedItems: 15502, StagedBytes: 877 << 20, LargestPage: 10 << 20}
	read := func() map[string]any {
		t.Helper()
		routes := routesOf(t, mailJSON(t, e, "mail", "list", "--since", "2026-01-01"))
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
	if !strings.Contains(fix, "Whole-account WorkIQ mail sync is refused on this archive") || !strings.Contains(fix, "ourostack/m365crawl#262") ||
		!strings.Contains(fix, "350498 more items") || len(cmds) != 2 || strings.Contains(strings.Join(cmds, " "), "workiq sync") {
		t.Fatalf("a sync that cannot fit was still offered: %q %q", fix, cmds)
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
	mailFails(t, e, "db_error", 1, "mail", "list", "--since", "2026-01-01")
}

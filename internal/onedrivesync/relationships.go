package onedrivesync

import "context"

func relationships(ctx context.Context, result *Result, losses map[string]int, maxDepth int) error {
	scopes := make(map[string]bool, len(result.Scopes))
	folders := make(map[string]Folder, len(result.Folders))
	items := make(map[string]bool, len(result.Files)+len(result.Folders))
	policies := make(map[[3]string]bool, len(result.Policies))
	for _, scope := range result.Scopes {
		scopes[scope.ID] = true
	}
	for _, folder := range result.Folders {
		folders[folder.ID] = folder
		items[folder.ID] = true
	}
	for _, file := range result.Files {
		items[file.ID] = true
	}
	for _, policy := range result.Policies {
		policies[[3]string{policy.SiteID, policy.WebID, policy.ListID}] = true
	}

	walk := func(start string, isFolder bool) (string, error) {
		seen := map[string]bool{}
		current := start
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			folder, exists := folders[current]
			if scopes[current] {
				if exists {
					return "parent_unresolved", nil
				}
				return "", nil
			}
			if !exists {
				return "parent_unresolved", nil
			}
			if seen[current] {
				if isFolder && current == start {
					return "parent_cycle", nil
				}
				return "parent_unresolved", nil
			}
			if len(seen) >= maxDepth {
				return "", &ReadError{Code: "onedrive_sync_index_too_large"}
			}
			seen[current] = true
			current = folder.ParentID
		}
	}
	for _, folder := range result.Folders {
		code, err := walk(folder.ID, true)
		if err != nil {
			return err
		}
		if code != "" {
			losses["onedrive_sync_"+code]++
		}
	}
	for _, file := range result.Files {
		code, err := walk(file.ParentID, false)
		if err != nil {
			return err
		}
		if code != "" {
			losses["onedrive_sync_"+code]++
		}
	}
	for _, graph := range result.Graph {
		if !items[graph.ResourceID] {
			losses["onedrive_sync_graph_orphan"]++
		}
	}
	for _, hydration := range result.Hydration {
		if !items[hydration.ResourceID] {
			losses["onedrive_sync_hydration_orphan"]++
		}
	}
	for _, scope := range result.Scopes {
		if !policies[[3]string{scope.SiteID, scope.WebID, scope.ListID}] {
			losses["onedrive_sync_scope_policy_unknown"]++
		}
	}
	return ctx.Err()
}

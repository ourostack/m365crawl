package officeregistry

import "slices"

type nativeNode struct {
	parent    int64
	name      string
	malformed bool
}

func applicationFor(nodes map[int64]nativeNode, id int64, depth int) (string, bool) {
	var path []string
	seen := map[int64]bool{}
	for {
		node, exists := nodes[id]
		if !exists || node.malformed || seen[id] || len(path) >= depth {
			return "", false
		}
		seen[id] = true
		path = append(path, node.name)
		if _, exists := nodes[node.parent]; !exists {
			if node.parent >= 0 || (node.name != "HKEY_CURRENT_USER" && node.name != "Software") {
				return "", false
			}
			break
		}
		id = node.parent
	}
	slices.Reverse(path)
	if path[0] == "HKEY_CURRENT_USER" {
		path = path[1:]
	}
	prefix := []string{"Software", "Microsoft", "Office", "15.0", "Common", "MruUserData", "UnsignedUser"}
	if len(path) != 11 || !slices.Equal(path[:7], prefix) || path[8] != "Local" || path[9] != "Documents" {
		return "", true
	}
	switch path[7] {
	case "Word", "Excel", "PowerPoint":
		return path[7], true
	}
	return "", true
}

// Package teamscrawl holds the agent guide that the binary embeds, so `teamscrawl skill` always
// prints the guide that belongs to the installed version.
package teamscrawl

import _ "embed"

// Skill is .agents/skills/teamscrawl/SKILL.md.
//
//go:embed .agents/skills/teamscrawl/SKILL.md
var Skill string

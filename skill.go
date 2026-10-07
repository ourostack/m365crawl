// Package m365crawl holds the agent guide that the binary embeds, so `m365crawl skill` always
// prints the guide that belongs to the installed version.
package m365crawl

import _ "embed"

// Skill is .agents/skills/m365crawl/SKILL.md.
//
//go:embed .agents/skills/m365crawl/SKILL.md
var Skill string

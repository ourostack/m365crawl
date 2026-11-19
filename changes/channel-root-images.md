### Fixed

- `content fetch image` no longer refuses inline PNGs solely because the archived message is a Teams standard/private channel root post. Select the team with `--team-id TEAM` or a message link's `groupId`; original selected PNG bytes retain native user/tenant/team/channel/message provenance, before/after checks and existing integrity/output protections. Receipts include `team_id` only for channel acquisitions. Shared channels and replies remain unsupported; native channel endpoint/shape qualification is not yet established.

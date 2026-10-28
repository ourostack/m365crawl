package workiq

import (
	"encoding/json"
	"strings"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func failure(code string) *errs.Coded {
	exit := errs.ExitRuntime
	switch code {
	case "workiq_hermetic_required", "workiq_not_installed", "workiq_binary_unsafe",
		"workiq_disabled", "workiq_signin_required", "workiq_eula_required", "workiq_consent_required",
		"workiq_identity_mismatch", "workiq_mail_account_missing", "workiq_multiple_mailboxes":
		exit = errs.ExitEnvironment
	case "workiq_account_invalid", "workiq_account_required":
		exit = errs.ExitUsage
	}
	return &errs.Coded{Code: code, Exit: exit,
		Message: "the optional WorkIQ source could not complete (" + code + ")",
		Fix:     "Inspect the WorkIQ setup separately, then retry the explicit command; no sign-in or agreement was performed."}
}

// Child text is classified only after scrubbing and is never part of a returned diagnostic.
func classify(raw []byte) error {
	encoded, _ := json.Marshal(string(raw))
	safe, _ := teamsdesktop.Scrub(encoded)
	text := strings.ToLower(string(safe))
	switch {
	case strings.Contains(text, "accept-eula"), strings.Contains(text, "eula required"):
		return failure("workiq_eula_required")
	case strings.Contains(text, "admin consent"), strings.Contains(text, "consent_required"):
		return failure("workiq_consent_required")
	case strings.Contains(text, "auth login"), strings.Contains(text, "interaction_required"):
		return failure("workiq_signin_required")
	case strings.Contains(text, "policy allowlist"):
		return failure("workiq_policy_denied")
	default:
		return failure("workiq_failed")
	}
}

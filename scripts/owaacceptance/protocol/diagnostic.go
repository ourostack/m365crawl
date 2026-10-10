package protocol

import "slices"

const DiagnosticName = "diagnostic.json"
const DiagnosticPendingName = "diagnostic-pending.json"

const (
	StageGuard     = "guard"
	StageDiscovery = "discovery"
	StageCapture   = "capture"
	StageCensus    = "independent_census"
	StageImport    = "import_repeat_compare"
	StageAccounts  = "global_accounts"
	StageStability = "source_stability"
	StageCleanup   = "cleanup"
)

type Diagnostic struct {
	Version      int              `json:"version"`
	Nonce        string           `json:"nonce"`
	Test         string           `json:"test"`
	BinaryHash   string           `json:"binary_hash"`
	BinaryNative [3]uint32        `json:"binary_native"`
	RootNative   [3]uint32        `json:"root_native"`
	Supervisor   Process          `json:"supervisor"`
	Child        Process          `json:"child"`
	Outcome      string           `json:"outcome"`
	Stage        string           `json:"stage"`
	Reason       string           `json:"reason"`
	Fields       []string         `json:"fields"`
	Omissions    map[string]int64 `json:"omissions"`
}

func DecodeDiagnostic(data []byte) (Diagnostic, error) {
	var d Diagnostic
	if strict(data, &d) != nil || d.Version != Version || d.Test != TestName || !hash(d.Nonce) || !hash(d.BinaryHash) || d.BinaryNative == [3]uint32{} || d.RootNative == [3]uint32{} || !process(d.Supervisor) || !process(d.Child) || d.Supervisor.PID == d.Child.PID || d.Outcome != "failed" || !DiagnosticStage(d.Stage) || !DiagnosticReason(d.Stage, d.Reason) || !DiagnosticFields(d.Fields) || !DiagnosticOmissions(d.Stage, d.Reason, d.Omissions) {
		return Diagnostic{}, refused()
	}
	return d, nil
}

func DiagnosticStage(stage string) bool {
	switch stage {
	case StageGuard, StageDiscovery, StageCapture, StageCensus, StageImport, StageAccounts, StageStability, StageCleanup:
		return true
	}
	return false
}

var diagnosticFields = []string{
	"internet_message_id", "conversation_id", "to_me", "subject", "sender_name", "sender_address", "preview", "in_reply_to", "received_at", "sent_at", "class", "importance", "ical_uid", "is_read", "read_state", "flag", "has_attachments", "body_state",
	"archive_query", "source_id", "assigned_key", "folder_root", "folder_parent", "folders", "messages", "folder_key", "body_html_gz", "body_text_version", "body_derivation_unproved", "body_text", "gone_at", "attachment_key", "recipient_order_role_kind", "attachments", "attachment_metadata", "fts", "conversation_groups", "coverage_time", "mail_coverage", "mail_absent", "recipient_orphan", "attachment_orphan", "fts_orphan", "observation_accounts", "archive_accounts", "row_ownership",
	"aggregate_mail_messages", "aggregate_mail_folders", "aggregate_mail_recipients", "aggregate_mail_attachments", "aggregate_mail_source_ids", "aggregate_mail_coverage", "aggregate_mail_absent", "aggregate_mail_fts",
	"ToRecipients", "CcRecipients", "BccRecipients", "Attachments", "UID", "InternetMessageId",
}

func DiagnosticFields(fields []string) bool {
	if fields == nil || len(fields) > len(diagnosticFields) {
		return false
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if seen[field] || !slices.Contains(diagnosticFields, field) {
			return false
		}
		seen[field] = true
	}
	return true
}

func MatchDiagnostic(o Ownership, d Diagnostic) error {
	data, err := Encode(d)
	if err != nil {
		return refused()
	}
	if _, err := DecodeDiagnostic(data); err != nil || ValidateOwnership(o) != nil || !o.Adopted {
		return refused()
	}
	if o.Nonce != d.Nonce || o.Test != d.Test || o.BinaryHash != d.BinaryHash || o.BinaryNative != d.BinaryNative || o.RootNative != d.RootNative || o.Supervisor != d.Supervisor || o.Child != d.Child {
		return refused()
	}
	return nil
}

func DiagnosticFor(o Ownership, stage string, fields []string) Diagnostic {
	return Diagnostic{Version: Version, Nonce: o.Nonce, Test: o.Test, BinaryHash: o.BinaryHash, BinaryNative: o.BinaryNative, RootNative: o.RootNative, Supervisor: o.Supervisor, Child: o.Child, Outcome: "failed", Stage: stage, Reason: "unproved", Fields: fields, Omissions: map[string]int64{}}
}

// CensusReason recognizes only exact fixed errors from the independent census and HTML evidence helpers. It neither unwraps nor forwards arbitrary error text.
func CensusReason(err error) string {
	if err == nil {
		return "unproved"
	}
	return FixedDiagnosticReason(StageCensus, err.Error())
}

// These literal tables are shared by producer mapping and strict stage-bound wire validation.
var diagnosticReasons = map[string][]string{
	StageCensus: {
		"census_metadata_refused", "census_layout_refused", "census_open_refused", "census_close_refused", "census_decoder_refused", "census_context_refused", "census_resource_refused", "aggregate_payload_budget",
		"census_identity_refused", "census_duplicate_refused", "census_folder_refused", "census_folder_join_refused", "census_folder_cycle_refused", "census_folder_root_refused",
		"census_field_type_refused", "census_duplicate_field_refused", "census_text_refused", "census_boolean_refused", "census_array_refused", "census_date_refused",
		"detail_metadata_coverage_refused", "census_flag_refused", "census_attachment_flag_refused", "body_derivation_unproved", "census_recipient_refused", "census_attachment_identity_refused", "census_size_refused",
		"body_derivation_resource_refused", "body_derivation_encoding_unproved", "body_tokenization_unproved", "body_foreign_content_unproved", "body_rawtext_semantics_unproved",
	},
	StageGuard:     {"qualification_protocol_refused"},
	StageDiscovery: {"profile_discovery_refused", "no_observed_profiles"},
	StageCapture:   {"source_resource_or_context_refused", "source_fingerprint_failed", "snapshot_refused", "snapshot_shape_failed", "private_source_mutated", "payload_resource_or_context_refused", "payload_digest_failed"},
	StageImport:    {"sync_refused", "sync_source_refused", "sync_status_mismatch", "sync_counts_mismatch", "archive_open_failed", "archive_state_refused", "archive_state_budget", "query_projection_budget", "repeat_archive_unstable", "archive_mismatch"},
	StageAccounts:  {"archive_open_failed", "account_isolation_mismatch", "query_projection_budget"},
	StageStability: {"unstable_source_observation"},
	StageCleanup:   {"snapshot_cleanup_failed", "archive_cleanup_failed", "archive_close_failed"},
}

func FixedDiagnosticReason(stage, code string) string {
	for _, literal := range diagnosticReasons[stage] {
		if code == literal {
			return literal
		}
	}
	return "unproved"
}

func DiagnosticReason(stage, reason string) bool {
	return DiagnosticStage(stage) && (reason == "unproved" || FixedDiagnosticReason(stage, reason) != "unproved")
}

var diagnosticLossCodes = []string{
	"outlook_mail_unmapped", "outlook_mail_layout_partial", "outlook_mail_folder_missing",
	"outlook_owa_duplicate_conflict", "outlook_owa_detail_conflict", "outlook_owa_root_uncertain", "outlook_owa_source_changed",
	"outlook_owa_reader_omission", "outlook_owa_reader_truncated_log_tail", "outlook_owa_reader_empty_value", "outlook_owa_reader_unknown_envelope",
	"outlook_owa_reader_snappy_too_large", "outlook_owa_reader_blob_missing", "outlook_owa_reader_bad_key", "outlook_owa_reader_v8_unknown_tag",
	"outlook_owa_reader_v8_host_object", "outlook_owa_reader_v8_shared", "outlook_owa_reader_v8_version", "outlook_owa_reader_v8_malformed",
}

func DiagnosticOmissions(stage, reason string, omissions map[string]int64) bool {
	if omissions == nil || len(omissions) > len(diagnosticLossCodes) || len(omissions) != 0 && (stage != StageImport || reason != "sync_source_refused") {
		return false
	}
	var total int64
	for code, count := range omissions {
		if !slices.Contains(diagnosticLossCodes, code) || count <= 0 || count > MaxCount-total {
			return false
		}
		total += count
	}
	return true
}

// Copy only literal keys after bounding the source report map; unknown input is refused as a whole.
func DiagnosticSourceOmissions(input map[string]int) (map[string]int64, bool) {
	if len(input) > len(diagnosticLossCodes) {
		return map[string]int64{}, false
	}
	safe := map[string]int64{}
	var total int64
	for _, literal := range diagnosticLossCodes {
		if count, found := input[literal]; found {
			if count <= 0 || int64(count) > MaxCount-total {
				return map[string]int64{}, false
			}
			safe[literal] = int64(count)
			total += int64(count)
		}
	}
	if len(safe) != len(input) {
		return map[string]int64{}, false
	}
	return safe, true
}

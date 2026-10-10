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
	Version      int       `json:"version"`
	Nonce        string    `json:"nonce"`
	Test         string    `json:"test"`
	BinaryHash   string    `json:"binary_hash"`
	BinaryNative [3]uint32 `json:"binary_native"`
	RootNative   [3]uint32 `json:"root_native"`
	Supervisor   Process   `json:"supervisor"`
	Child        Process   `json:"child"`
	Outcome      string    `json:"outcome"`
	Stage        string    `json:"stage"`
	Reason       string    `json:"reason"`
	Fields       []string  `json:"fields"`
}

func DecodeDiagnostic(data []byte) (Diagnostic, error) {
	var d Diagnostic
	if strict(data, &d) != nil || d.Version != Version || d.Test != TestName || !hash(d.Nonce) || !hash(d.BinaryHash) || d.BinaryNative == [3]uint32{} || d.RootNative == [3]uint32{} || !process(d.Supervisor) || !process(d.Child) || d.Supervisor.PID == d.Child.PID || d.Outcome != "failed" || !DiagnosticStage(d.Stage) || !DiagnosticReason(d.Stage, d.Reason) || !DiagnosticFields(d.Fields) {
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
	return Diagnostic{Version: Version, Nonce: o.Nonce, Test: o.Test, BinaryHash: o.BinaryHash, BinaryNative: o.BinaryNative, RootNative: o.RootNative, Supervisor: o.Supervisor, Child: o.Child, Outcome: "failed", Stage: stage, Reason: "unproved", Fields: fields}
}

// CensusReason recognizes only exact fixed errors from the independent census and HTML evidence helpers. It neither unwraps nor forwards arbitrary error text.
func CensusReason(err error) string {
	if err == nil {
		return "unproved"
	}
	return censusReasonConstant(err.Error())
}

func censusReasonConstant(code string) string {
	// One exact-switch whitelist serves both mapping and wire validation; return constants, never arbitrary error text.
	switch code {
	case "census_metadata_refused":
		return "census_metadata_refused"
	case "census_layout_refused":
		return "census_layout_refused"
	case "census_open_refused":
		return "census_open_refused"
	case "census_close_refused":
		return "census_close_refused"
	case "census_decoder_refused":
		return "census_decoder_refused"
	case "census_context_refused":
		return "census_context_refused"
	case "census_resource_refused":
		return "census_resource_refused"
	case "census_identity_refused":
		return "census_identity_refused"
	case "census_duplicate_refused":
		return "census_duplicate_refused"
	case "census_folder_refused":
		return "census_folder_refused"
	case "census_folder_join_refused":
		return "census_folder_join_refused"
	case "census_folder_cycle_refused":
		return "census_folder_cycle_refused"
	case "census_folder_root_refused":
		return "census_folder_root_refused"
	case "census_field_type_refused":
		return "census_field_type_refused"
	case "census_duplicate_field_refused":
		return "census_duplicate_field_refused"
	case "census_text_refused":
		return "census_text_refused"
	case "census_boolean_refused":
		return "census_boolean_refused"
	case "census_array_refused":
		return "census_array_refused"
	case "census_date_refused":
		return "census_date_refused"
	case "detail_metadata_coverage_refused":
		return "detail_metadata_coverage_refused"
	case "census_flag_refused":
		return "census_flag_refused"
	case "census_attachment_flag_refused":
		return "census_attachment_flag_refused"
	case "body_derivation_unproved":
		return "body_derivation_unproved"
	case "census_recipient_refused":
		return "census_recipient_refused"
	case "census_attachment_identity_refused":
		return "census_attachment_identity_refused"
	case "census_size_refused":
		return "census_size_refused"
	case "body_derivation_resource_refused":
		return "body_derivation_resource_refused"
	case "body_derivation_encoding_unproved":
		return "body_derivation_encoding_unproved"
	case "body_tokenization_unproved":
		return "body_tokenization_unproved"
	case "body_foreign_content_unproved":
		return "body_foreign_content_unproved"
	case "body_rawtext_semantics_unproved":
		return "body_rawtext_semantics_unproved"
	}
	return "unproved"
}

func DiagnosticReason(stage, reason string) bool {
	return reason == "unproved" || stage == StageCensus && censusReasonConstant(reason) != "unproved"
}

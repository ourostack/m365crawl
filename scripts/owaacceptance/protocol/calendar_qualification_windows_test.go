package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeCalendarQualification(owner Ownership) CalendarQualification {
	value := calendarQualified()
	value.Nonce, value.Test, value.BinaryHash = owner.Nonce, owner.Test, owner.BinaryHash
	value.BinaryNative, value.RootNative = owner.BinaryNative, owner.RootNative
	value.Supervisor, value.Child = owner.Supervisor, owner.Child
	return value
}

func TestCalendarQualificationNativeGuardAndCreateOnceEmission(t *testing.T) {
	owner, receipt := observationContext(t, CalendarQualificationTest)
	got, err := CurrentCalendarQualificationGuard()
	if err != nil || got != owner {
		t.Fatal("valid_distinct_calendar_qualification_context_refused")
	}
	if _, err := CurrentGuard(); err == nil {
		t.Fatal("calendar_qualification_became_mail_context")
	}
	if _, err := CurrentCalendarObservationGuard(); err == nil {
		t.Fatal("calendar_qualification_became_observation_context")
	}
	value := nativeCalendarQualification(owner)
	if EmitCalendarQualification(owner, value) != nil {
		t.Fatal("complete_guarded_qualification_emission_refused")
	}
	wire, err := ReadProtected(filepath.Join(receipt, CalendarQualificationName))
	if err != nil {
		t.Fatal("private_product_qualification_unreadable")
	}
	decoded, err := DecodeCalendarQualification(wire)
	if err != nil || MatchCalendarQualification(owner, decoded) != nil {
		t.Fatal("emitted_product_qualification_identity_or_coverage_refused")
	}
	if EmitCalendarQualification(owner, value) == nil {
		t.Fatal("product_qualification_replaced")
	}
	after, err := ReadProtected(filepath.Join(receipt, CalendarQualificationName))
	if err != nil || string(after) != string(wire) {
		t.Fatal("refused_duplicate_changed_product_qualification")
	}
}

func TestCalendarQualificationNativeEmissionRefusesOldScopes(t *testing.T) {
	for _, test := range []string{TestName, CalendarObservationTest} {
		t.Run(test, func(t *testing.T) {
			owner, receipt := observationContext(t, test)
			if _, err := CurrentCalendarQualificationGuard(); err == nil {
				t.Fatal("old_native_context_became_product_qualification")
			}
			if EmitCalendarQualification(owner, nativeCalendarQualification(owner)) == nil {
				t.Fatal("old_native_scope_emitted_product_qualification")
			}
			if _, err := os.Stat(filepath.Join(receipt, CalendarQualificationName)); !os.IsNotExist(err) {
				t.Fatal("old_native_scope_created_product_witness")
			}
		})
	}
}

func TestCalendarQualificationNativeEmissionRejectsUnprovedEvidence(t *testing.T) {
	owner, receipt := observationContext(t, CalendarQualificationTest)
	for _, mode := range []string{"readers", "mail", "mismatches", "raw", "nonce", "owner", "oversize"} {
		value, supplied := nativeCalendarQualification(owner), owner
		switch mode {
		case "readers":
			value.ValueReadersReleased = false
		case "mail":
			value.MailControlPassed = false
		case "mismatches":
			value.Mismatches = 1
		case "raw":
			value.Counts["records"]++
		case "nonce":
			value.Nonce = strings.Repeat("c", 64)
		case "owner":
			supplied.Child.Created++
		case "oversize":
			value.Counts[strings.Repeat("synthetic-private", MaxRecordBytes)] = 1
		}
		if err := EmitCalendarQualification(supplied, value); err == nil || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatal("unproved_or_unbound_product_witness_emitted_or_disclosed")
		}
	}
	if _, err := os.Stat(filepath.Join(receipt, CalendarQualificationName)); !os.IsNotExist(err) {
		t.Fatal("refusal_created_product_qualification")
	}
}

func TestCalendarQualificationNativeGuardDoesNotSkipContextChecks(t *testing.T) {
	owner, receipt := observationContext(t, CalendarQualificationTest)
	t.Setenv("M365CRAWL_UNAPPROVED", "synthetic-private")
	if _, err := CurrentCalendarQualificationGuard(); err == nil || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("unapproved_environment_accepted_or_disclosed")
	}
	if EmitCalendarQualification(owner, nativeCalendarQualification(owner)) == nil {
		t.Fatal("invalid_native_context_emitted_product_qualification")
	}
	if _, err := os.Stat(filepath.Join(receipt, CalendarQualificationName)); !os.IsNotExist(err) {
		t.Fatal("invalid_native_context_created_product_witness")
	}
}

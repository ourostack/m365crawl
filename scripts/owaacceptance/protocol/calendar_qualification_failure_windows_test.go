package protocol

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCalendarQualificationFailureNativeEmissionIsBoundAndCreateOnce(t *testing.T) {
	owner, receipt := observationContext(t, CalendarQualificationTest)
	value := calendarFailure()
	value.Owner = owner
	bad := value
	bad.Owner.Child.Created++
	if EmitCalendarQualificationFailure(owner, bad) == nil {
		t.Fatal("unbound_product_failure_emitted")
	}
	if _, err := os.Stat(filepath.Join(receipt, CalendarQualificationFailureName)); !os.IsNotExist(err) {
		t.Fatal("unbound_product_failure_created_record")
	}
	if EmitCalendarQualificationFailure(owner, value) != nil {
		t.Fatal("valid_private_product_failure_refused")
	}
	wire, err := ReadProtected(filepath.Join(receipt, CalendarQualificationFailureName))
	if err != nil {
		t.Fatal("protected_product_failure_unreadable")
	}
	decoded, err := DecodeCalendarQualificationFailure(wire)
	if err != nil || MatchCalendarQualificationFailure(owner, decoded) != nil {
		t.Fatal("protected_product_failure_binding_refused")
	}
	if EmitCalendarQualificationFailure(owner, value) == nil {
		t.Fatal("product_failure_record_replaced")
	}
	after, err := ReadProtected(filepath.Join(receipt, CalendarQualificationFailureName))
	if err != nil || string(after) != string(wire) {
		t.Fatal("duplicate_failure_write_mutated_prior_record")
	}
}

func TestCalendarQualificationFailureNativeEmissionRejectsOldContext(t *testing.T) {
	owner, receipt := observationContext(t, CalendarObservationTest)
	value := calendarFailure()
	value.Owner = owner
	if EmitCalendarQualificationFailure(owner, value) == nil {
		t.Fatal("observation_context_emitted_product_failure")
	}
	if _, err := os.Stat(filepath.Join(receipt, CalendarQualificationFailureName)); !os.IsNotExist(err) {
		t.Fatal("old_scope_created_product_failure")
	}
}

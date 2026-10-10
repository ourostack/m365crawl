package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func validOwner() Ownership {
	return Ownership{Version: Version, Nonce: strings.Repeat("a", 64), Test: TestName, BinaryHash: strings.Repeat("b", 64), BinaryNative: [3]uint32{1, 0, 2}, Root: `C:\private\owned`, RootNative: [3]uint32{1, 0, 3}, Source: `C:\private\source`, Supervisor: Process{PID: 1, Created: 2}, Child: Process{PID: 3, Created: 4}, Adopted: true}
}

func validWitness() Qualification {
	o := validOwner()
	counts := map[string]int64{}
	for _, field := range CountFields {
		counts[field] = 0
	}
	counts["messages"] = 5
	return Qualification{Version: Version, Nonce: o.Nonce, Test: TestName, BinaryHash: o.BinaryHash, BinaryNative: o.BinaryNative, RootNative: o.RootNative, Supervisor: o.Supervisor, Child: o.Child, Outcome: "passed", Observed: 1, Counts: counts}
}

func TestOwnershipStrictContract(t *testing.T) {
	o := validOwner()
	data, err := Encode(o)
	if err != nil {
		t.Fatal("encode_failed")
	}
	if _, err := DecodeOwnership(data); err != nil {
		t.Fatal("valid_owner_refused")
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { delete(v, "nonce") },
		func(v map[string]any) { v["extra"] = "private-never-output" },
		func(v map[string]any) { v["version"] = 2 },
		func(v map[string]any) { v["test"] = "unmatched" },
		func(v map[string]any) { v["nonce"] = "invalid" },
		func(v map[string]any) { v["binary_hash"] = "invalid" },
		func(v map[string]any) { v["root_native"] = []int{0, 0, 0} },
		func(v map[string]any) { v["child"] = map[string]any{"pid": 0, "created": 0} },
	} {
		var value map[string]any
		if json.Unmarshal(data, &value) != nil {
			t.Fatal("test_input_failed")
		}
		mutate(value)
		bad, _ := json.Marshal(value)
		if _, err := DecodeOwnership(bad); err == nil {
			t.Error("invalid_ownership_accepted")
		}
	}
	for _, bad := range [][]byte{append(append([]byte(nil), data...), []byte("{}")...), []byte(strings.Repeat(" ", MaxRecordBytes+1)), []byte(`{"version":`)} {
		if _, err := DecodeOwnership(bad); err == nil {
			t.Error("invalid_ownership_wire_accepted")
		}
	}
}

func TestQualificationStrictCountsAndMatch(t *testing.T) {
	w := validWitness()
	data, _ := Encode(w)
	if _, err := DecodeQualification(data); err != nil {
		t.Fatal("valid_witness_refused")
	}
	if Match(validOwner(), w) != nil {
		t.Fatal("valid_match_refused")
	}
	for _, mutate := range []func(*Qualification){
		func(v *Qualification) { v.Nonce = strings.Repeat("c", 64) },
		func(v *Qualification) { v.BinaryHash = strings.Repeat("c", 64) },
		func(v *Qualification) { v.Test = "unmatched" },
		func(v *Qualification) { v.Child.Created++ },
		func(v *Qualification) { v.RootNative[2]++ },
	} {
		value := validWitness()
		mutate(&value)
		if Match(validOwner(), value) == nil {
			t.Error("forged_witness_matched")
		}
	}
	for _, mutate := range []func(*Qualification){
		func(v *Qualification) { v.Counts["messages"] = -1 },
		func(v *Qualification) { v.Counts["extra"] = 1 },
		func(v *Qualification) { delete(v.Counts, "messages") },
		func(v *Qualification) { v.Counts["messages"] = MaxCount + 1 },
		func(v *Qualification) { v.Observed = 0 },
		func(v *Qualification) { v.Unstable = 1 },
		func(v *Qualification) { v.Outcome = "failed" },
	} {
		value := validWitness()
		mutate(&value)
		bad, _ := Encode(value)
		if _, err := DecodeQualification(bad); err == nil {
			t.Error("invalid_counts_witness_accepted")
		}
	}
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	fields["extra"] = "private-never-output"
	bad, _ := json.Marshal(fields)
	if _, err := DecodeQualification(bad); err == nil {
		t.Error("extra_witness_field_accepted")
	}
}

func TestCloseoutRetainedRecoveryContract(t *testing.T) {
	c := Closeout{Version: Version, Owner: validOwner(), Outcome: "retained", Reason: "lifecycle_refused", Exit: -1, RootState: "retained_owned", ObservedRootNative: validOwner().RootNative}
	data, _ := Encode(c)
	if _, err := DecodeCloseout(data); err != nil {
		t.Fatal("retained_evidence_refused")
	}
	c.Qualified = true
	data, _ = Encode(c)
	if _, err := DecodeCloseout(data); err == nil {
		t.Fatal("retained_payload_qualified")
	}
}

func TestWireRejectsDuplicateCountsAndWrongNativeLength(t *testing.T) {
	data, _ := Encode(validWitness())
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(data), `"messages":5`, `"messages":99,"messages":5`, 1)),
		[]byte(strings.Replace(string(data), `"root_native":[1,0,3]`, `"root_native":[1,0,3,99]`, 1)),
		[]byte(strings.Replace(string(data), `"root_native":[1,0,3]`, `"root_native":[1,0]`, 1)),
		[]byte(strings.Replace(string(data), `"root_native":[1,0,3]`, `"root_native":[1,null,3]`, 1)),
		append(append([]byte(nil), data...), []byte("{}")...),
		[]byte(strings.Repeat(" ", MaxRecordBytes+1)),
		[]byte(strings.Replace(string(data), `"observed":1`, `"observed":null`, 1)),
	} {
		if _, err := DecodeQualification(bad); err == nil {
			t.Error("ambiguous_witness_wire_accepted")
		}
	}
}

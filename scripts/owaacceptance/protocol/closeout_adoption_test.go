package protocol

import "testing"

func TestQualifiedCloseoutRequiresAdoptedChild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adopted bool
		child   Process
	}{
		{"prelaunch_empty", false, Process{}},
		{"prelaunch_child", false, Process{PID: 3, Created: 4}},
		{"adopted_empty", true, Process{}},
		{"adopted_missing_creation", true, Process{PID: 3}},
		{"adopted_supervisor_child", true, Process{PID: 1, Created: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := validOwner()
			owner.Adopted, owner.Child = tc.adopted, tc.child
			closeout := Closeout{Version: Version, Owner: owner, Outcome: "passed", Reason: "none", Exit: 0, ConsumersReleased: true, NativeCloseVerified: true, RootState: "absent", Qualified: true}
			data, err := Encode(closeout)
			if err != nil {
				t.Fatal("synthetic_closeout_encode_failed")
			}
			if _, err := DecodeCloseout(data); err == nil {
				t.Fatal("qualified_without_adopted_child_accepted")
			}
		})
	}
	owner := validOwner()
	witness := validWitness()
	if Match(owner, witness) != nil {
		t.Fatal("adopted_witness_contract_refused")
	}
	data, _ := Encode(Closeout{Version: Version, Owner: owner, Outcome: "passed", Reason: "none", Exit: 0, ConsumersReleased: true, NativeCloseVerified: true, RootState: "absent", Qualified: true})
	if _, err := DecodeCloseout(data); err != nil {
		t.Fatal("valid_adopted_closeout_refused")
	}
}

func TestPreAdoptionCloseoutRefusalAndRecoveryRemainValid(t *testing.T) {
	owner := validOwner()
	owner.Adopted, owner.Child = false, Process{}
	for _, tc := range []struct{ outcome, root string }{
		{"refused", "absent"}, {"retained", "retained_owned"}, {"retained", "retained_unverified"},
	} {
		t.Run(tc.outcome+"_"+tc.root, func(t *testing.T) {
			record := Closeout{Version: Version, Owner: owner, Outcome: tc.outcome, Reason: "lifecycle_refused", Exit: -1, RootState: tc.root}
			if tc.root == "retained_owned" {
				record.ObservedRootNative = owner.RootNative
			}
			data, _ := Encode(record)
			if _, err := DecodeCloseout(data); err != nil {
				t.Fatal("legitimate_pre_adoption_recovery_refused")
			}
			record.Qualified = true
			data, _ = Encode(record)
			if _, err := DecodeCloseout(data); err == nil {
				t.Fatal("pre_adoption_refusal_qualified")
			}
		})
	}
}

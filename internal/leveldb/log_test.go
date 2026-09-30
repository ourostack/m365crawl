package leveldb

import "testing"

func TestApplyBatchMalformed(t *testing.T) {
	s := store{}
	good := batch(1, "a", "1", "b", "2")
	if !applyBatch(good, s) || len(s) != 2 {
		t.Fatalf("good batch: %v", s)
	}
	s2 := store{}
	// Cut mid-record: earlier records survive, result reports malformed.
	if applyBatch(good[:len(good)-2], s2) {
		t.Fatal("truncated batch accepted")
	}
	if _, ok := s2["a"]; !ok {
		t.Fatal("record before defect lost")
	}
	if applyBatch([]byte{1, 2, 3}, store{}) {
		t.Fatal("short batch accepted")
	}
	bad := append([]byte(nil), good[:12]...)
	bad = append(bad, 9) // unknown record type
	if applyBatch(bad, store{}) {
		t.Fatal("unknown type accepted")
	}
}

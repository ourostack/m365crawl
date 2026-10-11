package outlookowa

import "testing"

func TestMapper3RootRulesInvalidateMapper2Fingerprint(t *testing.T) {
	p := fixtureProfile(t, sourceTempDir(t), "Synthetic", "mail", true)
	saved := mapperVersion
	t.Cleanup(func() { mapperVersion = saved })
	mapperVersion = 2
	old, fresh, err := Fingerprint(p)
	if err != nil {
		t.Fatal("synthetic_old_fingerprint_failed")
	}
	mapperVersion = MapperVersion
	current, currentFresh, err := Fingerprint(p)
	if MapperVersion != 3 || err != nil || old == current || !fresh.Equal(currentFresh) {
		t.Fatal("mapper3_root_fingerprint_not_invalidated")
	}
}

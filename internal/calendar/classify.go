package calendar

import "testing"

// AssertEveryFieldClassified is for a mapper's test: it fails t for every field of the Unknown
// vocabulary that e neither states (carries a value for) nor lists in Unknown, unless the test
// names it in knownEmpty, the fields the source stated as empty. A field in none of the three is a
// silent "none": the mapper forgot to say it did not read the field, and Capture would treat the
// event as knowing it is empty. It also fails for an event whose UnknownDeclared is false.
func AssertEveryFieldClassified(t testing.TB, e Event, knownEmpty ...Field) {
	t.Helper()
	if !e.UnknownDeclared {
		t.Errorf("the mapper did not set UnknownDeclared")
	}
	empty := unknownSet{}
	for _, f := range knownEmpty {
		empty[f] = true
	}
	for _, f := range unknownVocabulary {
		if !e.unknown(f) && !valued[f](e) && !empty[f] {
			t.Errorf("field %q is neither stated, listed unknown, nor named as known empty", f)
		}
	}
}

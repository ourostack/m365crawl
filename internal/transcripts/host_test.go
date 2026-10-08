package transcripts

import "testing"

func TestSharePointHostsOnly(t *testing.T) {
	for _, h := range []string{"contoso.sharepoint.com", "Contoso.SharePoint.com", "a-b.sharepoint.us", "x.sharepoint-mil.us", "x.sharepoint.cn", "x.sharepoint.de", "tenant" + testSuffix} {
		if !IsSharePointHost(h) {
			t.Errorf("%s is a SharePoint host", h)
		}
	}
	for _, h := range []string{"attacker.example", "sharepoint.com", ".sharepoint.com", "evilsharepoint.com", "sharepoint.com.attacker.example", "x.sharepoint.com/", "x.sharepoint.com:443",
		"x.sharepoint.com.", "", "x.sharepoint.org", "u@x.sharepoint.com"} {
		if IsSharePointHost(h) {
			t.Errorf("%q is not a SharePoint host", h)
		}
	}
	// A cached notice that names any other host is unfetchable, with the malformed reason.
	p := tpart("a"+testSuffix, "I1", 1)
	if !p.Fetchable() {
		t.Fatal("the fixture host must pass")
	}
	p.Host = "attacker.example"
	if p.ValidRef() || p.Fetchable() || Reason(p, PartState(p, "", false)) != "the cached file reference is malformed" {
		t.Fatalf("%+v must be unfetchable as malformed", p)
	}
	// Without the test allowance the synthetic host is refused too.
	restore := AllowHostSuffixForTests("")
	defer restore()
	if IsSharePointHost("tenant" + testSuffix) {
		t.Fatal("the test suffix is allowed only while a test asks for it")
	}
}

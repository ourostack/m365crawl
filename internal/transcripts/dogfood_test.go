package transcripts

import (
	"context"
	"strings"
	"testing"
)

func TestDogfoodSharePointReferences(t *testing.T) {
	for _, host := range []string{
		"microsoft-my.sharepoint-df.com",
		"MICROSOFT-MY.SHAREPOINT-DF.COM",
		"contoso.sharepoint-df.com",
	} {
		t.Run(host, func(t *testing.T) {
			p := tpart(host, "I1", 1)
			p.SiteRoot = "/personal/ada_contoso_example"
			if !p.ValidRef() || !p.Fetchable() || PartState(p, "", false) != StateNotFetched {
				t.Fatal("a dogfood SharePoint drive-item reference must be fetchable")
			}
		})
	}
}

func TestDogfoodSharePointRejectsUnsafeReferences(t *testing.T) {
	tests := []struct {
		name, field, value, reason string
	}{
		{"lookalike suffix", "host", "contoso-my.sharepoint-df.com.attacker.invalid", "the cached file reference names an unsupported SharePoint host"},
		{"lookalike domain", "host", "contoso-my.evilsharepoint-df.com", "the cached file reference names an unsupported SharePoint host"},
		{"bare domain", "host", "sharepoint-df.com", "the cached file reference names an unsupported SharePoint host"},
		{"empty tenant", "host", ".sharepoint-df.com", "the cached file reference names an unsupported SharePoint host"},
		{"empty label", "host", "contoso..sharepoint-df.com", "the cached file reference names an unsupported SharePoint host"},
		{"leading hyphen", "host", "-contoso.sharepoint-df.com", "the cached file reference names an unsupported SharePoint host"},
		{"trailing hyphen", "host", "contoso-.sharepoint-df.com", "the cached file reference names an unsupported SharePoint host"},
		{"oversize label", "host", strings.Repeat("a", 64) + ".sharepoint-df.com", "the cached file reference names an unsupported SharePoint host"},
		{"scheme", "host", "https://contoso.sharepoint-df.com", "the cached file reference names an unsupported SharePoint host"},
		{"port", "host", "contoso.sharepoint-df.com:443", "the cached file reference names an unsupported SharePoint host"},
		{"path", "host", "contoso.sharepoint-df.com/personal/ada", "the cached file reference names an unsupported SharePoint host"},
		{"credentials", "host", "user@contoso.sharepoint-df.com", "the cached file reference names an unsupported SharePoint host"},
		{"trailing dot", "host", "contoso.sharepoint-df.com.", "the cached file reference names an unsupported SharePoint host"},
		{"empty host", "host", "", "the cached file reference names an unsupported SharePoint host"},
		{"invalid site", "site", "/personal/ada?next=elsewhere", "the cached file reference is malformed"},
		{"missing site", "site", "", "the cached file reference is malformed"},
		{"invalid drive", "drive", "drive/elsewhere", "the cached file reference is malformed"},
		{"missing drive", "drive", "", "the cached file reference is malformed"},
		{"invalid item", "item", "item?next=elsewhere", "the cached file reference is malformed"},
		{"missing item", "item", "", "the cached file reference is malformed"},
		{"invalid transcript", "transcript", "transcript#fragment", "the cached file reference is malformed"},
		{"missing transcript", "transcript", "", "the cached file reference is malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tpart("contoso-my.sharepoint-df.com", "I1", 1)
			p.SiteRoot = "/personal/ada_contoso_example"
			switch tt.field {
			case "host":
				p.Host = tt.value
			case "site":
				p.SiteRoot = tt.value
			case "drive":
				p.DriveID = tt.value
			case "item":
				p.ItemID = tt.value
			case "transcript":
				p.TranscriptID = tt.value
			}
			if p.ValidRef() || p.Fetchable() || PartState(p, "", false) != StateUnfetchable {
				t.Fatal("an unsafe reference must remain unfetchable")
			}
			page := newFakePage()
			var s saved
			sum, err := newFetcher(page).Fetch(context.Background(), []Part{p}, s.save)
			if err != nil {
				t.Fatal(err)
			}
			if sum.Unfetchable != 1 || sum.Fetched != 0 || len(sum.Parts) != 1 ||
				sum.Parts[0].Reason != tt.reason || len(page.navigations) != 0 || page.evalCount() != 0 || len(s.parts) != 0 {
				t.Fatalf("unsafe reference reached retrieval or had the wrong diagnosis: %+v", sum)
			}
		})
	}
}

func TestFetchDogfoodSharePointEveryPart(t *testing.T) {
	first := tpart("CONTOSO-MY.SHAREPOINT-DF.COM", "I1", 1)
	first.CallID, first.SiteRoot = "call-one", "/personal/ada_contoso_example"
	preamble := tpart("contoso-my.sharepoint-df.com", "I2", 1)
	preamble.CallID, preamble.SiteRoot = "call-two", "/personal/ada_contoso_example"
	preamble.TranscribeOnly, preamble.ContentTypes = true, "Transcript"
	recorded := tpart("contoso-my.sharepoint-df.com", "I3", 2)
	recorded.CallID, recorded.SiteRoot = "call-two", "/personal/ada_contoso_example"
	recorded.ContentTypes = "Recording+Transcript"
	page := newFakePage()
	var s saved
	parts := []Part{first, preamble, recorded}
	var savedParts []Part
	sum, err := newFetcher(page).Fetch(context.Background(), parts, func(p Part, r FetchResult) error {
		savedParts = append(savedParts, p)
		return s.save(p, r)
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Fetched != 3 || sum.Unfetchable != 0 || sum.Failed != 0 || len(sum.Parts) != 3 ||
		strings.Join(s.parts, ",") != "I1,I2,I3" || page.evalCount() != 3 || len(savedParts) != 3 {
		t.Fatalf("every recorded and transcribe-only part must be fetched and saved: %+v", sum)
	}
	if strings.Join(page.navigations, ",") != "https://contoso-my.sharepoint-df.com/personal/ada_contoso_example/" {
		t.Fatalf("unexpected site navigation: %v", page.navigations)
	}
	for i, item := range []string{"I1", "I2", "I3"} {
		if sum.Parts[i].Part != parts[i] || savedParts[i] != parts[i] {
			t.Fatalf("part %d lost its native identity or metadata", i+1)
		}
		if o := sum.Parts[i]; o.State != StateOK || !o.Saved || o.Entries != 1 || !o.FetchedAt.Equal(fixedNow) {
			t.Fatalf("part %d did not retain its successful outcome: %+v", i+1, o)
		}
		if a := page.evals[i]; a.Base != "https://contoso-my.sharepoint-df.com/personal/ada_contoso_example/_api/v2.1/drives/b!d1/items/"+item ||
			a.TranscriptID != "tr-"+item {
			t.Fatalf("part %d used the wrong native selection: %+v", i+1, a)
		}
	}
}

func TestDogfoodSharePointStillRequiresDriveItem(t *testing.T) {
	for _, tt := range []struct{ quality, reason string }{
		{RefShareOnly, "only a sharing link is cached for this part; m365crawl cannot fetch it from ids"},
		{RefAMSOnly, "only a Teams media-service link is cached; it is not a SharePoint file"},
		{RefUnresolved, "Teams posted a transcript notice but no file reference"},
		{"unknown", "the cached file reference is malformed"},
	} {
		t.Run(tt.quality, func(t *testing.T) {
			p := tpart("contoso-my.sharepoint-df.com", "I1", 1)
			p.RefQuality = tt.quality
			page := newFakePage()
			var s saved
			sum, err := newFetcher(page).Fetch(context.Background(), []Part{p}, s.save)
			if err != nil {
				t.Fatal(err)
			}
			if p.Fetchable() || sum.Unfetchable != 1 || len(sum.Parts) != 1 || sum.Parts[0].Reason != tt.reason ||
				len(page.navigations) != 0 || page.evalCount() != 0 || len(s.parts) != 0 {
				t.Fatalf("a recognized host must not bypass reference quality: %+v", sum)
			}
		})
	}
}

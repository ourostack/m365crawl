package sharepointcontent

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

type admittedRequest struct {
	Kind, Host, Site, Path, TranscriptID string
}

func admit(request Request) (admittedRequest, error) {
	fail := func() (admittedRequest, error) {
		return admittedRequest{}, &ReadError{Code: "invalid_input"}
	}
	if request.Kind != "page" && request.Kind != "stream" {
		return fail()
	}
	if len(request.URL) > 8192 || !utf8.ValidString(request.URL) || strings.Contains(request.URL, "#") ||
		len(request.TranscriptID) > 256<<10 || !utf8.ValidString(request.TranscriptID) ||
		strings.ContainsRune(request.TranscriptID, 0) || (request.Kind == "page" && request.TranscriptID != "") {
		return fail()
	}
	u, err := url.Parse(request.URL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return fail()
	}
	host := strings.ToLower(u.Hostname())
	if !sharepointHost(host) || len(u.Path) > 4096 || !validPath(u.Path) {
		return fail()
	}
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return fail()
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fail()
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) < 4 || (parts[1] != "sites" && parts[1] != "teams") {
		return fail()
	}
	site := "/" + parts[1] + "/" + parts[2]
	path := u.Path
	if request.Kind == "page" {
		if len(parts) != 5 || !strings.EqualFold(parts[3], "SitePages") || !strings.HasSuffix(strings.ToLower(parts[4]), ".aspx") {
			return fail()
		}
	} else {
		if len(parts) != 6 || !strings.EqualFold(parts[3], "_layouts") || parts[4] != "15" || !strings.EqualFold(parts[5], "stream.aspx") {
			return fail()
		}
		ids := query["id"]
		if len(ids) != 1 || len(ids[0]) > 4096 || !validPath(ids[0]) || !strings.HasPrefix(ids[0], site+"/") {
			return fail()
		}
		path = ids[0]
	}
	return admittedRequest{Kind: request.Kind, Host: host, Site: site, Path: path, TranscriptID: request.TranscriptID}, nil
}

func sharepointHost(host string) bool {
	if len(host) > 253 || (!strings.HasSuffix(host, ".sharepoint.com") && !strings.HasSuffix(host, ".sharepoint-df.com")) {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func validPath(path string) bool {
	if !strings.HasPrefix(path, "/") || !utf8.ValidString(path) || strings.ContainsAny(path, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(path[1:], "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

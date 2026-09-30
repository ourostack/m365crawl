package teamsdesktop

import "strings"

// Account is one signed-in Teams identity. UserID is the bare user GUID: the "8:orgid:" prefix
// some database names carry is removed so an account has one key however Teams spells it.
type Account struct{ TenantID, UserID, Locale string }

const orgIDPrefix = "8:orgid:"

// ParseDatabaseName splits "Teams:<manager>:react-web-client:<tenant>:<user>:<locale>", where
// <user> may be "8:orgid:<uuid>". ok is false for any other name.
func ParseDatabaseName(name string) (manager string, acct Account, ok bool) {
	rest, found := strings.CutPrefix(name, "Teams:")
	if !found {
		return "", Account{}, false
	}
	parts := strings.Split(rest, ":")
	// manager, react-web-client, tenant, user (1 or 3 segments), locale
	if len(parts) < 5 || parts[1] != "react-web-client" || parts[0] == "" {
		return "", Account{}, false
	}
	tenant, locale := parts[2], parts[len(parts)-1]
	user := strings.Join(parts[3:len(parts)-1], ":")
	user = strings.TrimPrefix(user, orgIDPrefix)
	if tenant == "" || user == "" || locale == "" || strings.Contains(user, ":") {
		return "", Account{}, false
	}
	return parts[0], Account{TenantID: tenant, UserID: user, Locale: locale}, true
}

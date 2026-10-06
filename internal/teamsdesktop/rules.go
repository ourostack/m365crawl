package teamsdesktop

import "regexp"

// redacted replaces the secret part of a generic record's value.
const redacted = "[redacted]"

// ruleSet is every table that decides what Scrub redacts and what Denied refuses. There is one
// instance, rules, and Scrub and Denied read their rules from it and from nowhere else.
// MemoSignature hashes the whole of it by walking the fields with reflection, so a rule cannot
// change, and a field cannot be added, without the signature changing. To add a rule table, add
// a field here (of a kind the walker knows: string, int, bool, a regular expression, a slice, a
// map with string keys, a struct); a field of any other kind makes the signature panic, which a
// test catches. TestRuleTablesLiveInTheRegistry fails when scrub.go or deny.go declare a
// package-level variable or constant of their own.
type ruleSet struct {
	// Replacement is what a redaction writes in place of the secret.
	Replacement string
	// SecretKeys are the object keys (compared in lower case) whose value is always credential
	// material.
	SecretKeys map[string]bool
	// JWT matches a JSON Web Token: a header and a payload (both base64url JSON, so both start
	// with "eyJ") and a signature.
	JWT *regexp.Regexp
	// Bearer matches a whole JSON string that starts with "Bearer" and whitespace (an opaque
	// bearer token).
	Bearer *regexp.Regexp
	// SecretName matches a credential key name between quotes or escaped quotes inside text.
	SecretName *regexp.Regexp
	// Sig matches the value of a sig= query parameter (a signed URL's signature), any case.
	Sig *regexp.Regexp
	// MaxStringifiedDepth bounds how many levels of JSON-inside-a-string Scrub unwraps.
	MaxStringifiedDepth int
	// SiblingNameFields are the fields (compared in lower case) of an object whose value, when it
	// is a credential name, makes the SiblingValueField of the same object a secret.
	SiblingNameFields []string
	SiblingValueField string

	// DeniedPrefixes, DeniedTerms and DeniedTokens decide Denied. All are lower case. A name that
	// starts with a prefix or contains a term is denied; a token (too short to match as a
	// substring: "aad" is in "load") denies a name only as a whole segment, delimited by the start
	// or end of the name, one of - _ : . / or a camelCase boundary (userKeys has the segments user
	// and keys; monkeys has one). Over-denying is acceptable: every denied name is reviewed on the
	// work machine, and a wrongly denied one is cheaper than a decoded token.
	DeniedPrefixes []string
	DeniedTerms    []string
	DeniedTokens   []string
}

var rules = ruleSet{
	Replacement: redacted,
	SecretKeys: map[string]bool{
		"access_token": true, "refresh_token": true, "id_token": true,
		"authorization": true, "client_secret": true, "password": true,
	},
	JWT:                 regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`),
	Bearer:              regexp.MustCompile(`(?i)"bearer(?:\s|\\[tnr]|\\u00(?:09|0a|0d|20|a0))(?:[^"\\]|\\.)*"`),
	SecretName:          regexp.MustCompile(`(?i)["\\](?:access_token|refresh_token|id_token|authorization|client_secret|password)["\\]`),
	Sig:                 regexp.MustCompile(`(?i)([?&]sig=)[^&"\\\s#]+`),
	MaxStringifiedDepth: 4,
	SiblingNameFields:   []string{"type", "name", "key"},
	SiblingValueField:   "value",
	DeniedPrefixes:      []string{"teams:auth"},
	DeniedTerms: []string{
		"auth", "token", "credential", "secret", "cookie", "msal", "oneauth",
		"key-store", "keystore", "keyval", "session",
		"key-value", "keyring", "crypto", "encrypt", "pkce", "bearer", "password", "refresh", "adal",
	},
	DeniedTokens: []string{"aad", "keys", "jwt", "e2ee"},
}

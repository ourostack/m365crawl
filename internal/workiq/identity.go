package workiq

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// AccountFact is archived identity evidence, not a new account registration.
type AccountFact struct {
	ID                string   `json:"account_id"`
	Family            string   `json:"family"`
	UserID            string   `json:"-"`
	TenantID          string   `json:"-"`
	Addresses         []string `json:"-"`
	IdentityPresent   bool     `json:"-"`
	MultipleMailboxes bool     `json:"-"`
	// MatchMailbox, when set, is the account's sealed Windows mailbox identity: it says whether a
	// mailbox address and signed-in identity are that mailbox. It replaces Addresses as evidence.
	MatchMailbox func(mailboxAddress, userIdentity string) bool `json:"-"`
}

// mailboxAccount says the account is a further mailbox of a Windows profile (outlook/<p>/mailbox-N),
// which only its own recorded mailbox identity can bind.
func (fact AccountFact) mailboxAccount() bool {
	return strings.Contains(strings.TrimPrefix(fact.ID, "outlook/"), "/mailbox-")
}

type User struct {
	ID        string `json:"-"`
	Mail, UPN string `json:"-"`
}

type AccountBinding struct {
	Fact      AccountFact `json:"account"`
	UserID    string      `json:"-"`
	Address   string      `json:"-"`
	Bootstrap bool        `json:"-"`
}

// PinAddress chooses only archived identity evidence, never the default CLI user.
func (fact AccountFact) PinAddress() (string, error) {
	if fact.MultipleMailboxes {
		return "", failure("workiq_multiple_mailboxes")
	}
	if fact.MatchMailbox != nil {
		return "", nil // the sealed identity names no address; WorkIQ's own user is checked against it
	}
	addresses := make([]string, 0, len(fact.Addresses))
	for _, address := range fact.Addresses {
		addresses = append(addresses, strings.ToLower(strings.TrimSpace(address)))
	}
	sort.Strings(addresses)
	for _, address := range addresses {
		if address != "" && validAccount(address) {
			return address, nil
		}
	}
	if len(addresses) > 0 {
		return "", failure("workiq_account_invalid")
	}
	if fact.Family == "mail" && fact.IdentityPresent {
		return "", failure("workiq_identity_mismatch")
	}
	return "", nil
}

func DecodeUser(ctx context.Context, raw json.RawMessage) (User, error) {
	var wire struct{ ID, Mail, UserPrincipalName string }
	if _, err := decodeEntity(ctx, raw, &wire); err != nil {
		return User{}, err
	}
	if wire.ID == "" || len(wire.ID) > maxPath {
		return User{}, failure("workiq_protocol")
	}
	return User{ID: wire.ID, Mail: wire.Mail, UPN: wire.UserPrincipalName}, nil
}

func SelectMailAccount(user User, facts []AccountFact, selector string) (AccountBinding, error) {
	var matches []AccountBinding
	known, bootstrapNeeded := false, false
	for _, fact := range facts {
		if fact.Family != "mail" || !strings.HasPrefix(fact.ID, "outlook/") || len(fact.ID) == len("outlook/") ||
			selector != "" && selector != fact.ID {
			continue
		}
		known = true
		if selector != "" && fact.MultipleMailboxes {
			return AccountBinding{}, failure("workiq_multiple_mailboxes")
		}
		if !fact.IdentityPresent && fact.mailboxAccount() {
			if selector != "" {
				return AccountBinding{}, failure("workiq_account_invalid") // never bootstrapped to whoever WorkIQ has
			}
			continue
		}
		if !fact.IdentityPresent {
			bootstrapNeeded = true
			if selector == "" {
				continue
			}
			address, err := selectedAddress(user)
			if err != nil {
				return AccountBinding{}, err
			}
			matches = append(matches, AccountBinding{Fact: fact, UserID: user.ID, Address: address, Bootstrap: true})
			continue
		}
		matchedIdentity, address := false, ""
		if fact.MatchMailbox != nil {
			matchedIdentity, address = matchMailbox(fact, user)
			if matchedIdentity {
				matches = append(matches, AccountBinding{Fact: fact, UserID: user.ID, Address: address})
			}
			continue
		}
		for _, candidate := range []string{user.Mail, user.UPN} {
			candidate = strings.ToLower(strings.TrimSpace(candidate))
			matched := false
			for _, stored := range fact.Addresses {
				matched = matched || strings.EqualFold(strings.TrimSpace(stored), candidate)
			}
			if candidate == "" || !matched {
				continue
			}
			matchedIdentity = true
			if !validAccount(candidate) {
				continue
			}
			address = candidate
			break
		}
		if matchedIdentity {
			matches = append(matches, AccountBinding{Fact: fact, UserID: user.ID, Address: address})
		}
	}
	switch {
	case !known:
		return AccountBinding{}, failure("workiq_mail_account_missing")
	case len(matches) > 1:
		return AccountBinding{}, failure("workiq_account_required")
	case len(matches) == 1:
		if matches[0].Fact.MultipleMailboxes {
			return AccountBinding{}, failure("workiq_multiple_mailboxes")
		}
		if matches[0].Address == "" {
			return AccountBinding{}, failure("workiq_account_invalid")
		}
		if user.ID == "" {
			return AccountBinding{}, failure("workiq_protocol")
		}
		return matches[0], nil
	case bootstrapNeeded:
		return AccountBinding{}, failure("workiq_account_required")
	default:
		return AccountBinding{}, failure("workiq_identity_mismatch")
	}
}

// matchMailbox seals the WorkIQ user's own addresses as both the mailbox and the signed-in identity:
// only the user's own mailbox matches, never another mailbox the profile opens.
func matchMailbox(fact AccountFact, user User) (bool, string) {
	var addresses []string
	for _, address := range []string{user.Mail, user.UPN} {
		if address = strings.ToLower(strings.TrimSpace(address)); address != "" {
			addresses = append(addresses, address)
		}
	}
	for _, mailbox := range addresses {
		for _, signedIn := range addresses {
			if fact.MatchMailbox(mailbox, signedIn) {
				if !validAccount(mailbox) {
					return true, ""
				}
				return true, mailbox
			}
		}
	}
	return false, ""
}

func SelectChatAccount(user User, chat Chat, facts []AccountFact, selector string) (AccountBinding, error) {
	for _, fact := range facts {
		if fact.Family != "chats" || fact.UserID == "" || fact.UserID != user.ID || fact.TenantID == "" ||
			fact.TenantID != chat.TenantID || selector != "" && selector != fact.ID {
			continue
		}
		address, err := selectedAddress(user)
		if err != nil {
			return AccountBinding{}, err
		}
		return AccountBinding{Fact: fact, UserID: user.ID, Address: address}, nil
	}
	return AccountBinding{}, failure("workiq_chat_account_unmatched")
}

func selectedAddress(user User) (string, error) {
	for _, candidate := range []string{user.Mail, user.UPN} {
		address := strings.ToLower(strings.TrimSpace(candidate))
		if address != "" && validAccount(address) {
			return address, nil
		}
	}
	return "", failure("workiq_account_invalid")
}

// DiscoveryBinding pins only the already selected user. It is not an archived
// content owner; every chat still needs SelectChatAccount with its actual tenant.
func (user User) DiscoveryBinding() (AccountBinding, error) {
	if user.ID == "" {
		return AccountBinding{}, failure("workiq_protocol")
	}
	address, err := selectedAddress(user)
	if err != nil {
		return AccountBinding{}, err
	}
	return AccountBinding{UserID: user.ID, Address: address}, nil
}

func (binding AccountBinding) Verify(user User) error {
	if binding.UserID == "" || user.ID != binding.UserID || binding.Address == "" || !validAccount(binding.Address) ||
		!strings.EqualFold(binding.Address, strings.TrimSpace(user.Mail)) && !strings.EqualFold(binding.Address, strings.TrimSpace(user.UPN)) {
		return failure("workiq_identity_mismatch")
	}
	return nil
}

func (AccountFact) String() string      { return "WorkIQ archived account" }
func (AccountFact) GoString() string    { return "WorkIQ archived account" }
func (User) String() string             { return "WorkIQ private user" }
func (User) GoString() string           { return "WorkIQ private user" }
func (AccountBinding) String() string   { return "WorkIQ private binding" }
func (AccountBinding) GoString() string { return "WorkIQ private binding" }

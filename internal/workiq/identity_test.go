package workiq

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestWorkIQMailAccountBindingHonorsStoredIdentityAndExplicitBootstrap(t *testing.T) {
	me := User{ID: "user", Mail: "Owner@Example.Invalid", UPN: "upn@example.invalid"}
	facts := []AccountFact{
		{ID: "outlook/One", Family: "mail", IdentityPresent: true, Addresses: []string{"owner@example.invalid"}},
		{ID: "outlook/Empty", Family: "mail", IdentityPresent: true},
		{ID: "outlook/Failed", Family: "mail"},
	}
	binding, err := SelectMailAccount(me, facts, "")
	if err != nil || binding.Fact.ID != "outlook/One" || binding.UserID != me.ID || binding.Address != "owner@example.invalid" || binding.Bootstrap {
		t.Fatal("stored identity did not bind the existing matching account privately")
	}
	bootstrap, err := SelectMailAccount(me, facts, "outlook/Failed")
	if err != nil || !bootstrap.Bootstrap || bootstrap.Fact.ID != "outlook/Failed" || bootstrap.Address == "" {
		t.Fatal("explicit failed-attempt-only account could not bootstrap")
	}
	for _, selector := range []string{"outlook/Empty", "outlook/Missing"} {
		if _, err := SelectMailAccount(me, facts, selector); err == nil {
			t.Fatal("selector overrode known-empty identity or invented an account")
		}
	}
	other := me
	other.ID = "different-user"
	if err := binding.Verify(other); err == nil {
		t.Fatal("pinned session switched users")
	}
	other = me
	other.Mail, other.UPN = "other@example.invalid", ""
	if err := binding.Verify(other); err == nil {
		t.Fatal("pinned session switched addresses")
	}
	if err := binding.Verify(me); err != nil {
		t.Fatal("same pinned user was refused")
	}
}

func TestWorkIQAccountBindingAmbiguityCardinalityAndPrivateRepresentations(t *testing.T) {
	me := User{ID: "user", Mail: "owner@example.invalid"}
	one := AccountFact{ID: "outlook/One", Family: "mail", IdentityPresent: true, Addresses: []string{me.Mail}}
	two := one
	two.ID = "outlook/Two"
	_, err := SelectMailAccount(me, []AccountFact{one, two}, "")
	requireCode(t, err, "workiq_account_required")
	selected, err := SelectMailAccount(me, []AccountFact{one, two}, two.ID)
	if err != nil || selected.Fact.ID != two.ID {
		t.Fatal("explicit matching selector did not resolve ambiguity")
	}
	two.MultipleMailboxes = true
	_, err = SelectMailAccount(me, []AccountFact{two}, two.ID)
	requireCode(t, err, "workiq_multiple_mailboxes")
	_, err = SelectMailAccount(me, []AccountFact{two}, "")
	requireCode(t, err, "workiq_multiple_mailboxes")
	_, err = SelectMailAccount(me, nil, "")
	requireCode(t, err, "workiq_mail_account_missing")
	_, err = SelectMailAccount(me, []AccountFact{{ID: "outlook/Failed", Family: "mail"}}, "")
	requireCode(t, err, "workiq_account_required")
	for _, value := range []any{one, me, selected} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal("private account metadata could not encode")
		}
		for _, representation := range []string{string(encoded), fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value)} {
			if strings.Contains(representation, me.Mail) {
				t.Fatal("private sign-in address escaped through a data/debug representation")
			}
		}
	}
	for code, exit := range map[string]int{
		"workiq_identity_mismatch": errs.ExitEnvironment, "workiq_mail_account_missing": errs.ExitEnvironment,
		"workiq_multiple_mailboxes": errs.ExitEnvironment, "workiq_account_required": errs.ExitUsage,
	} {
		if failure(code).Exit != exit {
			t.Fatal("account refusal used the wrong documented exit family")
		}
	}
}

func TestWorkIQChatAccountBindingUsesActualGuestTenant(t *testing.T) {
	me := User{ID: "user", Mail: "owner@example.invalid"}
	facts := []AccountFact{
		{ID: "home/user", Family: "chats", TenantID: "home", UserID: "user"},
		{ID: "guest/user", Family: "chats", TenantID: "guest", UserID: "user"},
	}
	binding, err := SelectChatAccount(me, Chat{ID: "chat", TenantID: "guest"}, facts, "")
	if err != nil || binding.Fact.ID != "guest/user" {
		t.Fatal("guest chat was attached to the home tenant")
	}
	for _, tenant := range []string{"", "unknown"} {
		_, err := SelectChatAccount(me, Chat{ID: "chat", TenantID: tenant}, facts, "")
		requireCode(t, err, "workiq_chat_account_unmatched")
	}
	_, err = SelectChatAccount(me, Chat{ID: "chat", TenantID: "guest"}, facts, "home/user")
	requireCode(t, err, "workiq_chat_account_unmatched")
}

func TestWorkIQUserDecodeIsBoundedAndNotAnAddressDiagnostic(t *testing.T) {
	user, err := DecodeUser(context.Background(), []byte(`{"id":"user","mail":"owner@example.invalid","userPrincipalName":"upn@example.invalid"}`))
	if err != nil || user.ID != "user" || user.Mail == "" || user.UPN == "" {
		t.Fatal("typed /me identity lost required binding facts")
	}
	for _, raw := range []string{`{`, `{}`, `{"id":"one","id":"two"}`} {
		_, err := DecodeUser(context.Background(), []byte(raw))
		requireCode(t, err, "workiq_protocol")
	}
}

func TestWorkIQMailBindingSelectsAValidMatchingAddressWithoutOverride(t *testing.T) {
	me := User{ID: "user", Mail: "unsafe%address@example.invalid", UPN: "safe@example.invalid"}
	fact := AccountFact{ID: "outlook/One", Family: "mail", IdentityPresent: true, Addresses: []string{me.Mail, me.UPN}}
	binding, err := SelectMailAccount(me, []AccountFact{fact}, "")
	if err != nil || binding.Address != me.UPN {
		t.Fatal("an unsafe alias prevented selection of another valid stored /me match")
	}
}

func TestWorkIQAccountBindingFixedErrorsForMissingOrInvalidEvidence(t *testing.T) {
	fact := AccountFact{ID: "outlook/One", Family: "mail", IdentityPresent: true, Addresses: []string{"owner@example.invalid"}}
	_, err := SelectMailAccount(User{Mail: "owner@example.invalid"}, []AccountFact{fact}, "")
	requireCode(t, err, "workiq_protocol")
	fact.Addresses = []string{"unsafe%address@example.invalid"}
	_, err = SelectMailAccount(User{ID: "user", Mail: fact.Addresses[0]}, []AccountFact{fact}, "")
	requireCode(t, err, "workiq_account_invalid")
	_, err = SelectMailAccount(User{ID: "user"}, []AccountFact{{ID: "outlook/Failed", Family: "mail"}}, "outlook/Failed")
	requireCode(t, err, "workiq_account_invalid")
	_, err = SelectChatAccount(User{ID: "user"}, Chat{ID: "chat", TenantID: "tenant"},
		[]AccountFact{{ID: "tenant/user", Family: "chats", TenantID: "tenant", UserID: "user"}}, "")
	requireCode(t, err, "workiq_account_invalid")
	requireCode(t, (AccountBinding{UserID: "user"}).Verify(User{ID: "user"}), "workiq_identity_mismatch")
}

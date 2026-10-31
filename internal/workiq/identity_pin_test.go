package workiq

import (
	"context"
	"errors"
	"testing"
)

func TestWorkIQArchivedPinUsesOnlyValidatedKnownAddressEvidence(t *testing.T) {
	for _, tc := range []struct {
		fact AccountFact
		want string
		code string
	}{
		{AccountFact{Family: "mail", IdentityPresent: true, Addresses: []string{" Z@EXAMPLE.INVALID ", "a@example.invalid"}}, "a@example.invalid", ""},
		{AccountFact{Family: "mail", Addresses: []string{"bad&shell@example.invalid", "safe@example.invalid"}}, "safe@example.invalid", ""},
		{AccountFact{Family: "mail", Addresses: []string{"bad&shell@example.invalid"}}, "", "workiq_account_invalid"},
		{AccountFact{Family: "mail", IdentityPresent: true}, "", "workiq_identity_mismatch"},
		{AccountFact{Family: "mail", MultipleMailboxes: true}, "", "workiq_multiple_mailboxes"},
		{AccountFact{Family: "mail"}, "", ""},
		{AccountFact{Family: "chats", IdentityPresent: true}, "", ""},
	} {
		got, err := tc.fact.PinAddress()
		if got != tc.want {
			t.Fatal("archived pin selection used an invalid/default address or lost deterministic normalization")
		}
		if tc.code == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			requireCode(t, err, tc.code)
		}
	}
}

func TestWorkIQContextMutexRefusesCanceledAndStoppedWaits(t *testing.T) {
	var mu contextMutex
	stopped := make(chan struct{})
	if err := mu.lock(context.Background(), stopped); err != nil {
		t.Fatal(err)
	}
	close(stopped)
	requireCode(t, mu.lock(context.Background(), stopped), "workiq_failed")
	mu.unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	requireCode(t, mu.lock(ctx, make(chan struct{})), "workiq_timeout")
	client := &Client{p: &process{stop: make(chan struct{})}}
	requireCode(t, client.write(ctx, map[string]any{}), "workiq_timeout")
}

type completionInput struct{ wrote chan struct{} }

func (w completionInput) Write(p []byte) (int, error) {
	close(w.wrote)
	return len(p), nil
}
func (completionInput) Close() error { return nil }

func TestWorkIQProcessStopUnblocksAResponseWaitAfterCompletedWrite(t *testing.T) {
	wrote, stopped := make(chan struct{}), make(chan struct{})
	client := &Client{p: &process{input: completionInput{wrote: wrote}, stop: stopped}, frames: make(chan frame)}
	done := make(chan error, 1)
	go func() {
		_, err := client.call(context.Background(), requestTimeout, "initialize", nil)
		done <- err
	}()
	<-wrote
	if err := client.writeMu.lock(context.Background(), stopped); err != nil {
		t.Fatal(err)
	}
	close(stopped)
	client.writeMu.unlock()
	if err := <-done; err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("stopped process became a successful response or lost its owned-process failure")
	} else {
		requireCode(t, err, "workiq_failed")
	}
}

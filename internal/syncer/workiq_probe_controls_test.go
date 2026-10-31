package syncer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQProbeKindsRemainExplicitAndFamilyFailuresIndependent(t *testing.T) {
	for _, kind := range []string{"coverage", "status"} {
		t.Run(kind, func(t *testing.T) {
			db, _ := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			seedWorkIQChatOwner(t, db)
			t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Kind: kind})
			// Status names the Outlook accounts that need --account instead of failing; coverage refuses.
			if (err == nil) != (kind == "status") || len(report.Accounts) != 1 || report.Accounts[0].Family != "chats" {
				t.Fatal("mail binding refusal blocked independent matched chat probes", err)
			}
			assertWorkIQProbeHasNoData(t, db)
		})
	}
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	for _, options := range []WorkIQOptions{
		{DBPath: db, Kind: "write"}, {DBPath: db, Kind: "coverage", Source: "chats", Account: "missing/user"},
		{DBPath: db, Kind: "status", Source: "mail", Account: "missing/user"},
	} {
		if _, err := RunWorkIQ(context.Background(), options); err == nil {
			t.Fatal("invalid source kind/owner became a positive probe")
		}
	}
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkIQEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Kind: "coverage"}); err == nil {
		t.Fatal("disabled counts reached a source")
	}
}

func TestWorkIQProbeMalformedOrFailedPagesNeverQualifyData(t *testing.T) {
	for _, tc := range []struct {
		family, mode string
		failed       bool
	}{
		{"mail", "folder-fail", true}, {"mail", "bad-folder-page", true},
		{"mail", "bad-folder", true}, {"mail", "missing-total", false},
		{"chats", "bad-message", true}, {"chats", "bad-message-page", true},
		{"chats", "bad-message-cursor", true}, {"chats", "unknown-message-clock", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			if tc.family == "chats" {
				account = ""
				seedWorkIQChatOwner(t, db)
				t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			}
			t.Setenv("WORKIQ_PROBE_CASE", tc.mode)
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{
				DBPath: db, Source: tc.family, Account: account, Kind: "coverage", MaxChats: 1})
			if (err != nil) != tc.failed {
				t.Fatal("probe lost the actual page failure", err)
			}
			if !tc.failed && len(report.Gaps) == 0 {
				t.Fatal("unknown source counts were reported without a gap")
			}
			if tc.mode == "unknown-message-clock" && (len(report.Chats) != 1 || report.Chats[0].UnknownClock != 1 ||
				report.Chats[0].ServerCount != 0 || report.Chats[0].Complete) {
				t.Fatal("unknown or removed messages became counted complete content")
			}
			assertWorkIQProbeHasNoData(t, db)
		})
	}
}

func TestWorkIQStatusReportsOnlyTheInitialTestedPathClass(t *testing.T) {
	for _, family := range []string{"mail", "chats"} {
		t.Run(family, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			transcript := workIQAcquisitionStub(t)
			if family == "chats" {
				account = ""
				seedWorkIQChatOwner(t, db)
				t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			}
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: family, Account: account, Kind: "status"})
			if err != nil || len(report.Probes) != 1 || report.Probes[0].State != "allowed" {
				t.Fatal("explicit path probe failed", err)
			}
			raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "$skiptoken") || strings.Contains(string(raw), "/messages") {
				t.Fatal("status read content or followed a listing cursor")
			}
			assertWorkIQProbeHasNoData(t, db)
		})
	}
}

func TestWorkIQProbeReadWriteAndClientFaultsNeverReturnSuccess(t *testing.T) {
	for _, family := range []string{"mail", "chats"} {
		for _, kind := range []string{"coverage", "status"} {
			for _, role := range []string{"read", "write", "open", "close"} {
				t.Run(family+"-"+kind+"-"+role, func(t *testing.T) {
					firedAny := false
					for fail := 1; ; fail++ {
						reached := false
						func() {
							db, account := workIQAcquisitionArchive(t)
							workIQAcquisitionStub(t)
							if family == "chats" {
								account = ""
								seedWorkIQChatOwner(t, db)
								t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
							}
							releaseFixture := workIQBoundaryFixture(t, db)
							defer releaseFixture()
							oldRead, oldWrite, oldOpen, oldClose := workIQRead, workIQWrite, workIQOpen, workIQClose
							defer func() { workIQRead, workIQWrite, workIQOpen, workIQClose = oldRead, oldWrite, oldOpen, oldClose }()
							n, fired := 0, false
							broken := errors.New("synthetic counts/probe operation failure")
							switch role {
							case "read":
								workIQRead = func(ctx context.Context, path string, op func(*store.Store) error) error {
									n++
									if n == fail {
										fired = true
										return broken
									}
									return oldRead(ctx, path, op)
								}
							case "write":
								workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
									n++
									if n == fail {
										fired = true
										return broken
									}
									return oldWrite(ctx, path, op)
								}
							case "open":
								workIQOpen = func(ctx context.Context, invocation workiq.Invocation, address string) (*workiq.Client, error) {
									n++
									if n == fail {
										fired = true
										return nil, broken
									}
									return oldOpen(ctx, invocation, address)
								}
							case "close":
								workIQClose = func(client *workiq.Client) error {
									n++
									err := oldClose(client)
									if n == fail {
										fired = true
										return errors.Join(broken, err)
									}
									return err
								}
							}
							_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: family, Account: account, Kind: kind, MaxChats: 1})
							if fired && !errors.Is(err, broken) {
								t.Fatal("counts/probe swallowed an actual failed dependency", role, fail, err)
							}
							firedAny = firedAny || fired
							reached = fired
							assertWorkIQProbeHasNoData(t, db)
						}()
						if !reached {
							break
						}
					}
					if !firedAny {
						t.Fatal("fault injection never reached the counts/probe boundary")
					}
				})
			}
		}
	}
}

func TestWorkIQProbeStatusFailureHasFixedCodeWithoutRawErrors(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_PROBE_CASE", "folder-fail")
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account, Kind: "status"})
	if err == nil || len(report.Probes) != 1 || report.Probes[0].State != "failed" || report.Probes[0].Code == "" {
		t.Fatal("path-policy failure became generic positive status", err)
	}
	state, code := workIQProbeState(errors.New("synthetic private child text"))
	if state != "failed" || code != "workiq_failed" {
		t.Fatal("uncoded failure was exposed rather than mapped")
	}
	state, code = workIQProbeState(&errs.Coded{Code: "workiq_policy_denied"})
	if state != "failed" || code != "workiq_policy_denied" {
		t.Fatal("typed source refusal lost its fixed code")
	}
}

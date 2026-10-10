# Windows OWA acceptance process owner

This Windows-only supervisor runs the existing precompiled Go acceptance test, not another importer. Real OWA acceptance is still hard-blocked inside that test pending independent lifecycle review; this helper does not override the block. A completed child process is not, by itself, qualification evidence.

Build with `GOWORK=off go build -o <owned-supervisor.exe> ./scripts/owaacceptance` and `GOWORK=off go test -c -tags acceptance -o <owned-acceptance.test.exe> ./acceptance`. Use absolute binary paths. Never build a real-data capture into Git or run the entire real acceptance suite when qualifying only OWA mail.

```powershell
& <owned-supervisor.exe> -test-binary <owned-acceptance.test.exe> -outlook-root <measured-EBWebView> -scratch-parent <existing-private-validation-parent>
```

The scratch parent must exist outside a Git checkout, with no reparse ancestor. The supervisor creates a uniquely owned workspace, locks its DACL to the current user and SYSTEM before any child payload write, then records its native volume/file identity. Child TEMP/TMP/TMPDIR and home/default application-data paths are redirected there. The test receives an explicit Outlook source and a nonexistent Teams root. The source is read-only; the ordinary archive and browser profile are not used.

The child starts suspended and enters an unnamed kill-on-close Windows Job Object before its initial thread resumes. Standard output/error go to the null device, so even a crash cannot publish a raw payload. The test's existing two-minute cooperative work budget remains; the child test has a four-minute timeout and the supervisor terminates the owned job after five minutes. These are operational ceilings, not performance promises.

On normal, nonzero or timed-out child exit, the supervisor inventories job members, terminates remaining descendants, waits for the tracked process handles to signal and verifies zero active job processes. Only then does it inventory and remove exact non-reparse files and empty directories within the same native workspace identity. No process-name killing, recursive wildcard deletion or cleanup outside that workspace occurs. Missing binaries, ownership changes, reparse paths, ACL changes, unverified consumers or failed deletion return failure; an unverified-consumer failure leaves the protected workspace in place rather than deleting under a live reader.

This protects against abnormal **test-process** exits while the supervisor is alive. Supervisor termination closes the job handle and kills its descendants, but does not execute file cleanup; host crash/reboot also does not prove cleanup. Reconcile a retained protected workspace separately from recorded ownership evidence. Do not infer reboot durability or guaranteed removal after the supervisor itself is killed.

`GOWORK=off go test -count=1 ./scripts/owaacceptance` uses only synthetic payloads. It proves normal/nonzero/timeout disposal twice, terminates a descendant holding a payload file, preserves an unrelated process, verifies the private DACL, and refuses replacement roots, junctions, Git ancestors and relative scratch parents. The helper prints fixed status codes only; a later reviewed qualification receipt must independently prove the selected real test actually ran and its counts/relationships passed.

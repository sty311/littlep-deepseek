# Open-source readiness

This assessment records the source-package checks, re-run before the initial private GitHub upload on 2026-10-02. The repository is [sty311/littlep-deepseek](https://github.com/sty311/littlep-deepseek) and was created with **private** visibility. The original private stable versions and running device were not changed.

| Item | Status | Evidence / scope |
|---|---|---|
| Repository structure | PASS | Bilingual README, modular Go source, guarded patch/deploy tools, focused docs, tests and local CI template |
| Build status | PASS | CGO-disabled linux/arm64 bridge and supervisor; version-pinned local build hashes in `dist/SHA256SUMS` |
| Test status | PASS | Go mock tests/vet, Python guards, synthetic UI tests and Linux supervisor lifecycle tests |
| Secret scan | PASS | Two staged/archive scans, including comparison with private-project key and device identifiers; zero matching files |
| Copyright scan | PASS | No full vendor programs/resources; only authored wrapper and narrow patch recipes |
| Third-party license status | WARN | Original source is Apache-2.0; no external Go modules. Exact external page compiler rights/toolchain remain user's prerequisite, not a distributed dependency |
| Proprietary file scan | PASS | No firmware, ELF, vendor bytecode, certificates, original manifests or real device data in source set |
| Fresh clone test | PASS | Source ZIP extracted into an independent directory; make verify, lifecycle-test and package passed without private paths or input files |
| README commands | WARN | Build/test/verify/package passed in the clean copy; exact page/native dry-run passed on private originals. Device check/install/rollback exercised with synthetic mocks only |
| Device deployment of cleaned package | WARN | Private r7 baseline passed device acceptance; the newly configurable source package/installer were not installed during packaging |
| Repository visibility | PASS | GitHub API confirmed the new repository is private; no public publication or GitHub Release |

## Known limitations

- Only the exact documented X6 Pro/OS/app/files are allowlisted. Same version string alone is insufficient.
- Page compilation requires a user-supplied exact compatible Windows compiler; no fully open compiler reproducibility claim.
- Source license does not grant rights to redistribute vendor input/output or third-party services/software.
- Public installer uses guarded backups/rollback but still requires independent device validation; it refuses occupied prior installations rather than migrating them blindly.
- Memory-only context, model/API capability dependence, bounded formula conversion, no TTS and no persistent memory.
- Linux supervisor tests use synthetic processes. Earlier device tests were normal reboots, not power-cut tests.

See [security audit](SECURITY_AUDIT.md), [notices](NOTICE.md) and [development](docs/development.md). Before making the repository public, re-run `make verify`, `make lifecycle-test` and the source archive review.

## Initial private upload checks

- Re-ran `make verify` and all nine Linux lifecycle scenarios successfully.
- Re-scanned all 85 source files for credentials, the development device serial and owner-specific paths; no matching values were found.
- Normalized source text to LF before refreshing the checksum inventory so Git checkouts preserve the recorded digests.
- The initial source package remains version 0.7.0, based on the r7 lineage. Later private device work is not included in this upload.

## Local verification results

- Toolchain: Go 1.27.1 on Windows and Linux/WSL; Python and Node synthetic tests.
- 52 top-level Go test functions (plus subtests), go vet and gofmt checks; nine Python guard/deployment tests; 15 synthetic UI checks.
- Nine Linux lifecycle scenarios: occupied port, stale PID, unrelated live PID, repeated/concurrent start, shutdown, stop/disable, enable/restart, crash/privacy backoff, live rotation.
- Exact PC original-file dry-run reproduced native, wrapper and renamed-module target SHA256; generated manifest JSON matches the verified baseline semantically. No vendor output is shipped.
- Windows and Linux builds with the same pinned toolchain produced identical ARM64 binary digests:
  - Bridge: `62ca0478b1af9ea4a66e07b4ce860f78dd51d326ba23ba2727baa0ece8c58afb`
  - Supervisor: `13ae6ac21c52cb9fb7f6628a50b07b3dcd80377a26ed6c759b5c58abc3d3357f`
- Clean-copy tests used only declared tools, source and synthetic fixtures; optional proprietary-input compilation is separate and deliberately not required by CI.

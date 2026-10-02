# Contributing

Format Go with gofmt; use small modules and error-returning validation. After reviewing intentional source changes, refresh the source checksum inventory with `make inventory`, then run `make verify` and, for lifecycle changes, `make lifecycle-test`. This inventory refresh does not change the guarded vendor preimage/target manifests. Tests must use mock services and synthetic data, never a paid/live API or a connected device in CI.

Do not submit vendor firmware/binaries/bundles, compiler executables, real keys, device identifiers, account data, user screenshots or scans. Do not add permissive unknown-version patching, LAN defaults, TLS bypasses or rootfs changes. New device proposals should supply model/OS/DTB/SKU and file hashes, not firmware bodies or serials. Preserve independent rollback and document validation scope honestly.

Project contributions are licensed under Apache-2.0; only submit original work or code with clear compatible provenance/notices. Vendor imports do not confer vendor code ownership. Keep behavior changes separate from packaging/documentation changes.

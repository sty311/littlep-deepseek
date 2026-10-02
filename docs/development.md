# Development and local verification

Requirements: Go 1.20+, Python 3.10+, Node.js 18+, POSIX sh. Windows can build/test the bridge using Python directly; supervisor process tests require Linux/WSL. The packaging compiler was Go 1.27.1. No third-party Go modules are imported, so no `go.sum` is necessary. No vendor module or compiler is fetched by builds.

```sh
make test
make build
make verify
make lifecycle-test
make package
```

The same commands are available as `python3 scripts/project.py test|build|verify|audit|package`. `GO` and `GOFMT` can select a toolchain. Builds use CGO=0, linux/arm64, trimpath, disabled VCS stamping and an empty build ID; output includes `dist/SHA256SUMS`. Pin the same Go version for byte-identical builds. Runtime key is never compiled in.

Mock HTTP tests cover parser/vision/stream/tool/context flows. UI tests execute only the authored wrapper with synthetic components/text; vendor bytecode is not evaluated. The separate Linux lifecycle test builds a synthetic bridge and manipulates only temporary mock processes. Secret/binary/link scan, manifest validation and shell syntax are included in verification. No real API, ADB, UI reload or network upload runs in CI.

The local source packager excludes `.git`, `dist`, `private`, `backup`, `payload`, logs and caches; output is source-only. Fresh-clone verification extracts that ZIP into an independent directory and runs tests/build/checks there. The optional page build is separate: users must supply exact original files and exact compatible compiler; these are never included in the ZIP. See [patch instructions](../patch/README.md).

`SOURCE_SHA256SUMS` records the reviewed source set. After intentional, reviewed changes use `make inventory` to refresh it; `make verify` rejects missing, changed or unexpected source files. Vendor patch allowlists remain separate and must never be relaxed to accommodate unknown input.

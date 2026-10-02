# Project state

- Baseline: private r7-autostart-stable; preserved unchanged.
- Public package: source-ready 0.7.0; runtime private configuration replaces build-time key embedding.
- Device support: exact YDPX6-2 CHN PLUS / OS 4.3.5 / Little P 2.3.6 hashes only.
- Layout: `bridge/`, authored `patch/templates/`, guarded `patch/scripts/`, `deploy/`, `docs/`, synthetic `tests/`.
- Build: `make build`; linux/arm64, CGO=0. Tests require Go/Python/Node; lifecycle tests require Linux.
- API key: never in public source or compiled binaries; user's runtime `config.json` remains private.
- Rollback: `python3 deploy/device.py rollback --serial "$DEVICE_SERIAL" --backup backup/<timestamp>`.
- Remaining external prerequisite: exact user-supplied page compiler; public installer not redeployed during packaging.
- GitHub repository: `sty311/littlep-deepseek`, created private for the initial source upload. No public release, new features, TTS, device modification or private baseline migration.

# Source artifacts

| Artifact | Location |
|---|---|
| Bridge entry / modules | `bridge/cmd/littlep-bridge/`, `bridge/internal/bridge/` |
| Linux supervisor | `bridge/cmd/littlep-supervisor/` |
| Config template | `config/config.example.json` |
| Exact preimage / target digests and byte recipes | `patch/manifests/native.json`, `patch/manifests/ui.json` |
| Authored reasoning wrapper | `patch/templates/RobotMessage-e079798d.js` |
| Offline transformer / validation | `patch/scripts/` |
| Device identity, install, autostart, rollback | `deploy/device.py` |
| Management wrappers / project boot hook | `deploy/*.sh` |
| Build / test / audit / source package | `scripts/project.py`, `Makefile` |
| Synthetic UI / lifecycle / guard tests | `tests/` and bridge tests |
| Generated ARM64 binaries and SHA256 | Local ignored `dist/` only; excluded from source archive |
| Source ZIP / SHA256 | Local ignored `dist/littlep-deepseek-source-ready.zip` and `.sha256` |

Generated private patch payloads, vendor inputs, backups, live acceptance logs and the key-bearing private baseline binary are intentionally not listed as redistributable artifacts. Source archive includes an authored-source checksum inventory; all hashes describe files, never credentials.

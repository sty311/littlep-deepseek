# Installation

Use the [README quick start](../README.md#quick-start) from the repository root. Build/test tools never contact a device or model. Before device work, close Little P normally, keep the device charged and preserve backups privately.

1. Run the read-only device check; extract the three allowlisted original files into `private/original-app/` with their relative paths.
2. Run patch dry-run. A full UI build additionally requires the hash-matched compiler described in [patch/README.md](../patch/README.md). Generate `payload/` privately. Never bypass a rejected hash.
3. Run `make build`; edit private configuration, setting the key and retaining `127.0.0.1:18181`. Do not put secrets in example config or source.
4. Run `python3 deploy/device.py install --serial "$DEVICE_SERIAL" --payload payload --config private/config.json`. The script requires `APPLY`, rejects occupied/existing installations, verifies a full private app and registry backup, records original owner/mode/size/hashes, and validates every transfer. It starts a healthy bridge before replacing files.
5. The recovery record is `backup/<timestamp>/recovery.json`. Keep it and the entire backup. No process kill or reboot is automatic; perform a normal restart yourself to load the new library/page and re-authorize ADB through your usual procedure if needed.
6. After that reboot, manually run `adb -s "$DEVICE_SERIAL" shell /userdisk/littlep-bridge/start.sh` and check `status.sh`; until autostart is enabled, an OS reboot does not start the bridge. Test voice, scan, streaming, search, cancellation and follow-ups. Only then add autostart using `python3 deploy/device.py autostart --serial "$DEVICE_SERIAL" --backup backup/<timestamp>` and test two normal reboots. This verifies the original startup caller hash and requires an absent hook.

The installer writes only the fixed Little P files and a new `/userdisk/littlep-bridge/`. It does not modify the package registry, other applications or rootfs. If a step fails, stop and inspect the backup/status; an interrupted install can be recovered by the guarded [rollback](rollback.md). Public installer packaging tests are mock/PC-only, not a new claim of device deployment validation.

# Rollback and recovery

First exit Little P normally. From the repository root:

```sh
python3 deploy/device.py rollback --serial "$DEVICE_SERIAL" --backup backup/<timestamp>
```

Use the backup printed by installation. The script checks device/slot identity, unchanged registry, every backup preimage, and allowlisted current original/patched hashes before requesting `APPLY`. Unknown current files or damaged backups cause refusal. It disables the bridge, removes only the verified project boot hook and added renamed module, restores the original library/page/manifest with saved uid/gid/mode, and leaves service files and backups for inspection. No recursive deletion is used. A normal restart loads the originals.

To stop only the service: `/userdisk/littlep-bridge/disable.sh`. To restore service: `/userdisk/littlep-bridge/enable.sh`. If the bridge is disabled while the endpoint remains patched, Little P receives a local service error; restoring the original app restores its original backend.

If a registry/app update occurred after installation, automated rollback intentionally stops. Inspect versions and restore only your verified app backup using an appropriate supported procedure; never overwrite unrelated registration data. There is no firmware recovery or automatic flashing path in this project.

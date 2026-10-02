# User-supplied original file patches

No original or patched vendor binary/module is included. The native manifest contains a narrow AArch64 patch recipe; the UI manifest contains a small hash-guarded module rename recipe. `templates/RobotMessage-e079798d.js` is the project-authored wrapper, not the vendor bundle. Its vendor imports resolve only on your own compatible device.

Required originals in a private app directory:

- `libs/libbusiness_littlep_1755531922.so`
- `RobotMessage-e079798d.js.bin`
- `manifest.json`

Every original SHA256 must match the manifest. Unknown input is rejected before modification. The original itself is never overwritten. Dry-run reconstructs native/renamed outputs in memory and verifies target hashes; full UI compilation additionally checks a user-supplied compiler and authored source hash, and verifies compiled wrapper target SHA256.

```sh
python3 patch/scripts/patch.py --app private/original-app --dry-run
python3 patch/scripts/patch.py --app private/original-app --qjsc /path/to/qjsc --dry-run
python3 patch/scripts/patch.py --app private/original-app --qjsc /path/to/qjsc --output payload
```

The full compilation step must run with Windows Python (the hash-matched compiler is a Windows executable); WSL/Linux can run the source build/tests and compiler-free dry-run. Do not attempt to execute that Windows tool as a native Linux compiler. For example, in PowerShell from the repository root, use `python patch/scripts/patch.py --app private/original-app --qjsc /path/to/qjsc.exe --output payload`.

The accepted compiler is the compatible QuickJS 20200705 Windows executable, SHA256 `669130f59badad8fd08852faf9385ab26577550a874743246924644774900d17`. It is **not distributed or downloaded here**; users must independently obtain it with appropriate rights. A generic compiler labeled the same version is not automatically equivalent. Full page compilation was checked locally with that supplied tool; the source-only build/test does not require it. A fully open compatible compiler path remains a known limitation.

The module rename byte recipe does not evaluate vendor bytecode. `patch.py` uses only Python standard library and the supplied compiler to compile our source. It updates only the relevant local app checksum entries and keeps other metadata. Generated payload includes originals/patched checksum lists, but is a **private derivative**, not a source release.

Restore through [deploy rollback](../docs/rollback.md), never by guessed offsets or a different device image. Do not commit input files, payloads, compiler binaries or full vendor manifests.

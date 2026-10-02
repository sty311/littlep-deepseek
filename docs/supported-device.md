# Supported device

| Item | Required value |
|---|---|
| Product | Youdao Dictionary Pen X6 Pro |
| Model / SKU | YDPX6-2 CHN PLUS / OVERHEAD_X62_SKU_CHN_PLUS |
| OS | Dictionary Pen OS 4.3.5 |
| DTB model | Rockchip RK3562 MELON LP4 V10 Board |
| Userspace | Buildroot Linux, AArch64 |
| Little P | 8001707294117702, version 2.3.6, active b slot |

Same marketed version does not imply identical app files. [Allowlisted hashes](../patch/manifests/native.json) are authoritative for patching; the original UI/manifest/compiler hashes are in [ui.json](../patch/manifests/ui.json). Unknown files are rejected. No support is claimed for X7, S6, P5 or any other model.

`python3 deploy/device.py check --serial "$DEVICE_SERIAL"` is read-only. It checks root uid, `/Version`, DTB, SKU configuration and package registration; it prints only supported identity facts, never the complete system configuration or unique device identifiers. Unsupported identity causes failure before mutations. ADB authentication/root access are prerequisites, not project features.

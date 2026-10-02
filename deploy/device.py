#!/usr/bin/env python3
"""Checked PC-side ADB operations. No authentication, firmware or rootfs changes."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shlex
import subprocess
import sys
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[1]
APP = "/userdisk/miniapp/data/mini_app/pkg/8001707294117702/b"
REGISTRY = "/userdisk/miniapp/data/mini_app/pkg/packages.json"
BASE = "/userdisk/littlep-bridge"
HOOK = "/userdisk/skip_re/skip_login.sh"
CALLER = "/etc/init.d/S99_run_test_scripts"
CALLER_SHA = "684bec501879415f7536d0d07343bc04969237eff503734eb0079913ce19d241"
NATIVE = "libs/libbusiness_littlep_1755531922.so"
WRAPPER = "RobotMessage-e079798d.js.bin"
RENAMED = "RobotMessage-original-e079798d.js.bin"
FILES = (NATIVE, WRAPPER, RENAMED, "manifest.json")
MANAGEMENT = (
    "start.sh",
    "stop.sh",
    "restart.sh",
    "status.sh",
    "enable.sh",
    "disable.sh",
)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def q(value):
    return shlex.quote(str(value))


class Device:
    def __init__(self, serial, adb="adb"):
        if not serial:
            raise ValueError("explicit ADB serial required")
        self.command = [adb, "-s", serial]

    def run(self, *args, binary=False):
        p = subprocess.run(
            self.command + list(args),
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=300,
        )
        return (
            p.stdout
            if binary
            else p.stdout.decode("utf-8", errors="strict").replace("\r", "").strip()
        )

    def shell(self, command):
        return self.run("shell", command)

    def digest(self, path):
        return self.shell("sha256sum " + q(path)).split()[0]

    def absent(self, path):
        return (
            self.shell(
                "if test -e "
                + q(path)
                + "; then printf present; else printf absent; fi"
            )
            == "absent"
        )

    def stat(self, path):
        parts = self.shell("stat -c %u:%g:%a:%s " + q(path)).split(":")
        if len(parts) != 4 or not all(p.isdigit() for p in parts):
            raise ValueError("unknown file metadata")
        return dict(zip(("uid", "gid", "mode", "size"), parts))

    def identity(self):
        uid = self.shell("id -u")
        version = self.shell("cat /Version")
        dtb = self.shell("cat /proc/device-tree/model").rstrip("\x00")
        sku = self.shell("cat /data/cfg/sys_config.conf")
        sku_matches = (
            re.search(
                r"(?<![A-Za-z0-9_])OVERHEAD_X62_SKU_CHN_PLUS(?![A-Za-z0-9_])", sku
            )
            is not None
        )
        if (
            uid != "0"
            or version != "4.3.5"
            or dtb != "Rockchip RK3562 MELON LP4 V10 Board"
            or not sku_matches
        ):
            raise ValueError("unsupported OS/DTB/SKU or no authorized root shell")
        packages = json.loads(self.shell("cat " + q(REGISTRY)))["packages"]
        record = next((p for p in packages if p.get("appid") == "8001707294117702"), {})
        if (
            record.get("version") != "2.3.6"
            or record.get("b") is not True
            or record.get("installPath", "").rstrip("/") != APP
        ):
            raise ValueError("unsupported active app slot/version")
        # Only supported identity facts are displayed; never print full sys_config.
        return {
            "model": "YDPX6-2 CHN PLUS",
            "os": version,
            "dtb": dtb,
            "app_version": "2.3.6",
            "target": APP,
        }

    def closed(self):
        if "App(8001707294117702)" in self.shell("miniapp_cli memoryApp"):
            raise ValueError("exit Little P normally before modifying files")

    def replace(self, local, target, expected):
        if expected is None:
            if not self.absent(target):
                raise ValueError("new path already exists")
        elif self.digest(target) != expected:
            raise ValueError("current target SHA256 changed")
        stage = target + ".littlep-stage"
        if not self.absent(stage):
            raise ValueError("staging file already exists")
        digest = sha(local.read_bytes())
        self.run("push", str(local), stage)
        self.shell(
            'set -e; test "$(sha256sum '
            + q(stage)
            + ' | cut -d " " -f 1)" = '
            + q(digest)
            + "; chmod 644 "
            + q(stage)
            + "; chown 0:0 "
            + q(stage)
        )
        guard = (
            ("test ! -e " + q(target))
            if expected is None
            else (
                'test "$(sha256sum '
                + q(target)
                + ' | cut -d " " -f 1)" = '
                + q(expected)
            )
        )
        self.shell(
            "set -e; "
            + guard
            + "; mv "
            + q(stage)
            + " "
            + q(target)
            + '; test "$(sha256sum '
            + q(target)
            + ' | cut -d " " -f 1)" = '
            + q(digest)
        )


def confirm(message):
    print(message)
    if input("Type APPLY to continue: ").strip() != "APPLY":
        raise ValueError("not confirmed")


def check(d):
    result = d.identity()
    native = json.loads((ROOT / "patch/manifests/native.json").read_text())
    ui = json.loads((ROOT / "patch/manifests/ui.json").read_text())
    result["native_preimage_matches"] = (
        d.digest(APP + "/" + NATIVE) == native["original_sha256"]
    )
    result["ui_preimage_matches"] = (
        d.digest(APP + "/" + WRAPPER) == ui["original_sha256"]
    )
    result["manifest_preimage_matches"] = (
        d.digest(APP + "/manifest.json") == ui["manifest_original_sha256"]
    )
    print(json.dumps(result, indent=2))
    return result


def install(d, payload, config):
    result = check(d)
    if not all(
        result[k]
        for k in (
            "native_preimage_matches",
            "ui_preimage_matches",
            "manifest_preimage_matches",
        )
    ):
        raise ValueError("unknown/already modified app; refusing install")
    d.closed()
    if not d.absent(BASE):
        raise ValueError(
            "bridge directory already exists; preserve existing installation"
        )
    payload = Path(payload)
    config = Path(config)
    for file in FILES:
        if not (payload / file).is_file():
            raise ValueError("incomplete private payload")
    sums = {
        line.split("  ", 1)[1]: line.split("  ", 1)[0]
        for line in (payload / "patched.sha256").read_text().splitlines()
    }
    if set(sums) != set(FILES):
        raise ValueError("payload file set invalid")
    for file in FILES:
        if sha((payload / file).read_bytes()) != sums[file]:
            raise ValueError("payload checksum changed")
    # Derivative payload must independently match the allowlisted recipes.
    native = json.loads((ROOT / "patch/manifests/native.json").read_text())
    ui = json.loads((ROOT / "patch/manifests/ui.json").read_text())
    if (
        sums[NATIVE] != native["patched_sha256"]
        or sums[WRAPPER] != ui["wrapper_sha256"]
        or sums[RENAMED] != ui["renamed_sha256"]
        or sums["manifest.json"] != ui["manifest_patched_sha256"]
    ):
        raise ValueError("payload not a known patch")
    conf = json.loads(config.read_text("utf-8"))
    if (
        not conf.get("deepseek_api_key")
        or conf["deepseek_api_key"] == "YOUR_DEEPSEEK_API_KEY"
        or conf.get("listen") != "127.0.0.1:18181"
    ):
        raise ValueError("set a private key and preserve endpoint port")
    for file in ("littlep-bridge", "littlep-supervisor"):
        if not (ROOT / "dist" / file).is_file():
            raise ValueError("build required")
    confirm(
        "Supported device verified. Will back up app and registry; modify "
        + APP
        + " and create "
        + BASE
        + ". No reboot is performed."
    )
    backup = ROOT / "backup" / datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    backup.mkdir(parents=True, exist_ok=False)
    d.run("pull", "-a", APP, str(backup / "app"))
    d.run("pull", "-a", REGISTRY, str(backup / "packages.json"))
    records = []
    for file in FILES:
        target = APP + "/" + file
        exists = not d.absent(target)
        stat = d.stat(target) if exists else None
        old = d.digest(target) if exists else None
        if exists and sha((backup / "app" / file).read_bytes()) != old:
            raise ValueError("backup verification failed")
        records.append(
            {
                "name": file,
                "original_sha256": old,
                "patched_sha256": sums[file],
                "stat": stat,
            }
        )
    if sha((backup / "packages.json").read_bytes()) != d.digest(REGISTRY):
        raise ValueError("registry backup failed")
    # Record full file metadata and SHA256 for every backed-up application file.
    inventory = []
    for local in sorted((backup / "app").rglob("*")):
        if local.is_file():
            rel = local.relative_to(backup / "app").as_posix()
            remote = APP + "/" + rel
            inventory.append(
                {"name": rel, "sha256": sha(local.read_bytes()), "stat": d.stat(remote)}
            )
            if inventory[-1]["sha256"] != d.digest(remote):
                raise ValueError("app changed during backup")
    record = {
        "app": APP,
        "registry_sha256": d.digest(REGISTRY),
        "files": records,
        "app_inventory": inventory,
        "hook_added": False,
    }
    (backup / "recovery.json").write_text(
        json.dumps(record, indent=2) + "\n", encoding="utf-8"
    )
    print("Verified private backup:", backup)
    expected_meta = json.loads((backup / "app/manifest.json").read_text("utf-8"))
    for file in (WRAPPER, RENAMED):
        data = (payload / file).read_bytes()
        expected_meta["cert"][file] = {
            "size": len(data),
            "md5": hashlib.md5(data).hexdigest(),
        }
    expected_bytes = (
        json.dumps(expected_meta, ensure_ascii=False, indent=2) + "\n"
    ).encode()
    if sha(expected_bytes) != sums["manifest.json"]:
        raise ValueError("metadata changes exceed the patch recipe")
    # Start service before activating localhost client files.
    d.shell("mkdir " + q(BASE))
    for file in ("littlep-bridge", "littlep-supervisor"):
        local = ROOT / "dist" / file
        d.run("push", str(local), BASE + "/" + file)
        if d.digest(BASE + "/" + file) != sha(local.read_bytes()):
            raise ValueError("binary transfer mismatch")
        d.shell("chmod 755 " + q(BASE + "/" + file))
    for file in MANAGEMENT:
        d.run("push", str(ROOT / "deploy" / file), BASE + "/" + file)
        if d.digest(BASE + "/" + file) != sha((ROOT / "deploy" / file).read_bytes()):
            raise ValueError("management transfer mismatch")
        d.shell("chmod 755 " + q(BASE + "/" + file))
    d.run("push", str(config), BASE + "/config.json")
    if d.digest(BASE + "/config.json") != sha(config.read_bytes()):
        raise ValueError("configuration transfer mismatch")
    d.shell("chmod 600 " + q(BASE + "/config.json"))
    d.shell(BASE + "/start.sh")
    status = json.loads(d.shell(BASE + "/status.sh"))
    if not status.get("bridge_healthy"):
        raise ValueError("bridge failed health; originals untouched")
    for file in (RENAMED, NATIVE, WRAPPER, "manifest.json"):
        expected = next(r["original_sha256"] for r in records if r["name"] == file)
        d.replace(payload / file, APP + "/" + file, expected)
    print(
        "Installed. No process was killed. Use a normal device restart to load the plugin; then open Little P. Private recovery:",
        backup,
    )


def autostart(d, backup):
    d.identity()
    d.closed()
    recovery = Path(backup) / "recovery.json"
    record = json.loads(recovery.read_text())
    if d.digest(CALLER) != CALLER_SHA or not d.absent(HOOK):
        raise ValueError("unknown startup caller or occupied hook; refuse overwrite")
    if record.get("app") != APP:
        raise ValueError("wrong recovery record")
    confirm(
        "Create only "
        + HOOK
        + " using the existing firmware hook. Rootfs stays unchanged."
    )
    # Save recovery intent first so an interrupted upload remains recoverable.
    record["hook_added"] = True
    record["hook_sha256"] = sha((ROOT / "deploy/skip_login.sh").read_bytes())
    recovery.write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    d.shell("mkdir -p " + q(str(Path(HOOK).parent).replace("\\", "/")))
    d.replace(ROOT / "deploy/skip_login.sh", HOOK, None)
    d.shell("chmod 755 " + q(HOOK))
    print("Autostart enabled. No reboot performed.")


def rollback(d, backup):
    d.identity()
    d.closed()
    backup = Path(backup)
    record = json.loads((backup / "recovery.json").read_text())
    if record.get("app") != APP:
        raise ValueError("unknown recovery target")
    if d.digest(REGISTRY) != record["registry_sha256"]:
        raise ValueError(
            "app registry changed; do not overwrite unrelated registrations"
        )
    for item in record["files"]:
        file = item["name"]
        if file not in FILES:
            raise ValueError("unexpected recovery file")
        target = APP + "/" + file
        current = None if d.absent(target) else d.digest(target)
        if current not in (item["original_sha256"], item["patched_sha256"]):
            raise ValueError("unexpected current file; refuse rollback")
        if (
            item["original_sha256"]
            and sha((backup / "app" / file).read_bytes()) != item["original_sha256"]
        ):
            raise ValueError("backup corrupted")
    if (
        record.get("hook_added")
        and not d.absent(HOOK)
        and d.digest(HOOK) != record["hook_sha256"]
    ):
        raise ValueError("startup hook changed")
    confirm(
        "Restore only the recorded Little P files and disable this bridge. Backup is preserved."
    )
    if not d.absent(BASE + "/disable.sh"):
        d.shell(BASE + "/disable.sh")
    if record.get("hook_added") and not d.absent(HOOK):
        d.shell("rm " + q(HOOK))
    for item in record["files"]:
        target = APP + "/" + item["name"]
        current = None if d.absent(target) else d.digest(target)
        if current == item["original_sha256"]:
            continue
        if item["original_sha256"]:
            d.replace(backup / "app" / item["name"], target, item["patched_sha256"])
            s = item["stat"]
            d.shell(
                "chmod "
                + q(s["mode"])
                + " "
                + q(target)
                + "; chown "
                + q(s["uid"] + ":" + s["gid"])
                + " "
                + q(target)
            )
        else:
            d.shell("rm " + q(target))
    print(
        "Original app restored; bridge files and backup retained, service disabled. Normal restart loads the originals."
    )


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("action", choices=("check", "install", "autostart", "rollback"))
    p.add_argument("--serial", required=True)
    p.add_argument("--adb", default="adb")
    p.add_argument("--payload")
    p.add_argument("--config")
    p.add_argument("--backup")
    a = p.parse_args()
    d = Device(a.serial, a.adb)
    if a.action == "check":
        check(d)
    elif a.action == "install":
        if not a.payload or not a.config:
            p.error("--payload and --config required")
        install(d, a.payload, a.config)
    else:
        if not a.backup:
            p.error("--backup required")
        globals()[a.action](d, a.backup)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, subprocess.CalledProcessError) as e:
        print(
            "STOP: validation or operation failed; inspect backup/status before retrying.",
            file=sys.stderr,
        )
        sys.exit(1)

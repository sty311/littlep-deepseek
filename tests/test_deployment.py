"""Full install/rollback rehearsal on synthetic, in-memory files; no ADB."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "deployment", Path(__file__).resolve().parents[1] / "deploy/device.py"
)
d = importlib.util.module_from_spec(spec)
spec.loader.exec_module(d)


class MemoryDevice:
    def __init__(self, files):
        self.files = dict(files)
        self.operations = []

    def identity(self):
        return {"model": "YDPX6-2 CHN PLUS", "os": "4.3.5", "target": d.APP}

    def closed(self):
        pass

    def digest(self, path):
        return d.sha(self.files[path])

    def absent(self, path):
        return path not in self.files and not any(
            p.startswith(path + "/") for p in self.files
        )

    def stat(self, path):
        return {
            "uid": "0",
            "gid": "0",
            "mode": "644",
            "size": str(len(self.files[path])),
        }

    def run(self, *args):
        self.operations.append(args)
        if args[0] == "pull":
            source, target = args[2], Path(args[3])
            if source == d.APP:
                for name, data in self.files.items():
                    if name.startswith(source + "/"):
                        file = target / name[len(source) + 1 :]
                        file.parent.mkdir(parents=True, exist_ok=True)
                        file.write_bytes(data)
            else:
                target.write_bytes(self.files[source])
        elif args[0] == "push":
            self.files[args[2]] = Path(args[1]).read_bytes()
        else:
            raise AssertionError("unexpected simulated command")

    def shell(self, command):
        self.operations.append(("shell", command))
        if command == d.BASE + "/status.sh":
            return json.dumps({"bridge_healthy": True})
        if command.startswith("rm "):
            self.files.pop(command[3:], None)
        elif not (
            command.startswith(("mkdir ", "chmod "))
            or command in (d.BASE + "/start.sh", d.BASE + "/disable.sh")
        ):
            raise AssertionError("unexpected synthetic shell operation")
        return ""

    def replace(self, local, target, expected):
        current = None if self.absent(target) else self.digest(target)
        if current != expected:
            raise ValueError("synthetic preimage mismatch")
        self.files[target] = local.read_bytes()
        self.operations.append(("replace", target))


class DeploymentRehearsal(unittest.TestCase):
    def test_autostart_uses_known_caller_and_refuses_occupied_hook(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            (root / "deploy").mkdir()
            hook = root / "deploy/skip_login.sh"
            hook.write_text("#!/bin/sh\nexit 0\n")
            recovery = root / "recovery.json"
            recovery.write_text(json.dumps({"app": d.APP, "hook_added": False}))
            caller = b"synthetic existing startup caller"
            dev = MemoryDevice({d.CALLER: caller})
            with (
                patch.object(d, "ROOT", root),
                patch.object(d, "CALLER_SHA", d.sha(caller)),
                patch("builtins.input", return_value="APPLY"),
            ):
                d.autostart(dev, root)
                self.assertEqual(dev.files[d.HOOK], hook.read_bytes())
                self.assertTrue(json.loads(recovery.read_text())["hook_added"])
                before = list(dev.operations)
                with self.assertRaises(ValueError):
                    d.autostart(dev, root)
                self.assertEqual(dev.operations, before)
            self.assertEqual(dev.files[d.CALLER], caller)

    def test_install_backup_and_rollback_only_owned_paths(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            (root / "patch/manifests").mkdir(parents=True)
            (root / "dist").mkdir()
            (root / "deploy").mkdir()
            originals = {
                d.NATIVE: b"synthetic original native",
                d.WRAPPER: b"synthetic original wrapper",
                "manifest.json": (
                    json.dumps(
                        {"appid": "8001707294117702", "version": "2.3.6", "cert": {}},
                        indent=2,
                    )
                    + "\n"
                ).encode(),
            }
            generated = {
                d.NATIVE: b"synthetic patched native",
                d.WRAPPER: b"synthetic authored wrapper",
                d.RENAMED: b"synthetic renamed original",
            }
            metadata = json.loads(originals["manifest.json"])
            for file in (d.WRAPPER, d.RENAMED):
                metadata["cert"][file] = {
                    "size": len(generated[file]),
                    "md5": d.hashlib.md5(generated[file]).hexdigest(),
                }
            generated["manifest.json"] = (
                json.dumps(metadata, ensure_ascii=False, indent=2) + "\n"
            ).encode()
            native = {"patched_sha256": d.sha(generated[d.NATIVE])}
            ui = {
                "wrapper_sha256": d.sha(generated[d.WRAPPER]),
                "renamed_sha256": d.sha(generated[d.RENAMED]),
                "manifest_patched_sha256": d.sha(generated["manifest.json"]),
            }
            for name, data in [("native", native), ("ui", ui)]:
                (root / "patch/manifests" / f"{name}.json").write_text(json.dumps(data))
            for name in ("littlep-bridge", "littlep-supervisor"):
                (root / "dist" / name).write_bytes(b"synthetic own executable")
            for name in d.MANAGEMENT:
                (root / "deploy" / name).write_text("#!/bin/sh\nexit 0\n")
            cfg = root / "private-config.json"
            cfg.write_text(
                json.dumps(
                    {
                        "deepseek_api_key": "synthetic-test-key",
                        "listen": "127.0.0.1:18181",
                    }
                )
            )
            payload = root / "payload"
            payload.mkdir()
            for name, data in generated.items():
                file = payload / name
                file.parent.mkdir(parents=True, exist_ok=True)
                file.write_bytes(data)
            (payload / "patched.sha256").write_text(
                "".join(
                    d.sha(data) + "  " + name + "\n" for name, data in generated.items()
                )
            )
            remote = {d.APP + "/" + name: data for name, data in originals.items()}
            remote[d.REGISTRY] = b'{"packages":[]}'
            remote["/userdisk/another-app/keep"] = b"unrelated sentinel"
            dev = MemoryDevice(remote)
            with patch.object(d, "ROOT", root), patch.object(
                d,
                "check",
                return_value={
                    "native_preimage_matches": True,
                    "ui_preimage_matches": True,
                    "manifest_preimage_matches": True,
                },
            ), patch("builtins.input", return_value="APPLY"):
                d.install(dev, payload, cfg)
                backup = next((root / "backup").iterdir())
                self.assertEqual(
                    (backup / "app" / d.NATIVE).read_bytes(), originals[d.NATIVE]
                )
                self.assertTrue(
                    json.loads((backup / "recovery.json").read_text())["app_inventory"]
                )
                self.assertEqual(dev.files[d.APP + "/" + d.NATIVE], generated[d.NATIVE])
                d.rollback(dev, backup)
            for name, data in originals.items():
                self.assertEqual(dev.files[d.APP + "/" + name], data)
            self.assertNotIn(d.APP + "/" + d.RENAMED, dev.files)
            self.assertEqual(
                dev.files["/userdisk/another-app/keep"], b"unrelated sentinel"
            )
            self.assertTrue((backup / "recovery.json").exists())


if __name__ == "__main__":
    unittest.main()

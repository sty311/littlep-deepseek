import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("device", ROOT / "deploy/device.py")
device = importlib.util.module_from_spec(spec)
spec.loader.exec_module(device)


class DeviceChecks(unittest.TestCase):
    def mock_read(self, d, version="4.3.5"):
        commands = []
        native = json.loads((ROOT / "patch/manifests/native.json").read_text())
        ui = json.loads((ROOT / "patch/manifests/ui.json").read_text())
        data = {
            "id -u": "0",
            "cat /Version": version,
            "cat /proc/device-tree/model": "Rockchip RK3562 MELON LP4 V10 Board\x00",
            "cat /data/cfg/sys_config.conf": "hardware_type=OVERHEAD_X62_SKU_CHN_PLUS",
            "cat "
            + device.REGISTRY: json.dumps(
                {
                    "packages": [
                        {
                            "appid": "8001707294117702",
                            "version": "2.3.6",
                            "b": True,
                            "installPath": device.APP + "/",
                        }
                    ]
                }
            ),
            "sha256sum "
            + device.APP
            + "/"
            + device.NATIVE: native["original_sha256"]
            + " file",
            "sha256sum "
            + device.APP
            + "/"
            + device.WRAPPER: ui["original_sha256"]
            + " file",
            "sha256sum "
            + device.APP
            + "/manifest.json": ui["manifest_original_sha256"]
            + " file",
        }

        def read(command):
            commands.append(command)
            if command not in data:
                raise AssertionError("unexpected command, not read-only fixture")
            return data[command]

        d.shell = read
        return commands

    def test_readonly_device_check(self):
        d = device.Device("SYNTHETIC_DEVICE")
        commands = self.mock_read(d)
        r = device.check(d)
        self.assertTrue(r["native_preimage_matches"])
        self.assertTrue(r["ui_preimage_matches"])
        self.assertTrue(
            all(x.startswith(("cat ", "id ", "sha256sum ")) for x in commands)
        )

    def test_wrong_version_stops_before_mutation(self):
        d = device.Device("SYNTHETIC_DEVICE")
        commands = self.mock_read(d, "0.0.0")
        with self.assertRaises(ValueError):
            device.check(d)
        self.assertFalse(any("push" in x for x in commands))

    def test_confirmation_is_explicit(self):
        with patch("builtins.input", return_value="yes"):
            with self.assertRaises(ValueError):
                device.confirm("synthetic operation")

    def test_no_implicit_device_selection(self):
        with self.assertRaises(ValueError):
            device.Device("")


if __name__ == "__main__":
    unittest.main()

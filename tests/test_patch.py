import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    "patcher", ROOT / "patch/scripts/patch.py"
)
patcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(patcher)


class PatchSafety(unittest.TestCase):
    def test_unknown_native_rejected(self):
        with self.assertRaises(ValueError):
            patcher.patch_native(b"synthetic non-vendor bytes")

    def test_unknown_ui_rejected(self):
        with self.assertRaises(ValueError):
            patcher.rename_module(b"synthetic non-vendor bytes")

    def test_exact_byte_recipe_and_target_guard(self):
        real = patcher.manifest
        data = b"abcdefgh"
        fake = {
            "original_sha256": patcher.digest(data),
            "patched_sha256": patcher.digest(b"abXYefgh"),
            "changes": [
                {
                    "file_offset": "0x2",
                    "original_hex": b"cd".hex(),
                    "patched_hex": b"XY".hex(),
                }
            ],
        }
        try:
            patcher.manifest = lambda name: fake
            self.assertEqual(patcher.patch_native(data), b"abXYefgh")
            fake["patched_sha256"] = "0" * 64
            with self.assertRaises(ValueError):
                patcher.patch_native(data)
        finally:
            patcher.manifest = real


if __name__ == "__main__":
    unittest.main()

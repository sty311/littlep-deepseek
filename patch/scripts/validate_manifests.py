#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import re

spec = importlib.util.spec_from_file_location(
    "patcher", Path(__file__).with_name("patch.py")
)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
for name in ("native", "ui"):
    data = m.manifest(name)
    for key, value in data.items():
        if key.endswith("sha256") and not re.fullmatch("[0-9a-f]{64}", value):
            raise SystemExit("invalid SHA256")
native = m.manifest("native")
end = 0
for edit in sorted(native["changes"], key=lambda x: int(x["file_offset"], 16)):
    off = int(edit["file_offset"], 16)
    old, new = bytes.fromhex(edit["original_hex"]), bytes.fromhex(edit["patched_hex"])
    assert off >= end and len(old) == len(new) == edit["length"]
    end = off + len(old)
assert end <= native["file_size_before"]
ui = m.manifest("ui")
assert (
    m.digest((m.ROOT / "templates/RobotMessage-e079798d.js").read_bytes())
    == ui["wrapper_source_sha256"]
)
end = 0
for edit in ui["rename_edits"]:
    assert edit["offset"] >= end
    end = edit["offset"] + edit["remove"]
    assert re.fullmatch("[0-9a-f]{64}", edit["preimage_sha256"])
    bytes.fromhex(edit["new_hex"])
print("PASS patch manifests and authored wrapper hash")

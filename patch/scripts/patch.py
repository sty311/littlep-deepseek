#!/usr/bin/env python3
"""Offline transforms. Never loads/evaluates vendor code or contacts a device."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
NATIVE = "libs/libbusiness_littlep_1755531922.so"
WRAPPER = "RobotMessage-e079798d.js.bin"
RENAMED = "RobotMessage-original-e079798d.js.bin"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def manifest(name):
    return json.loads((ROOT / "manifests" / (name + ".json")).read_text("utf-8"))


def require(data, expected):
    if digest(data) != expected:
        raise ValueError("SHA256 is not an allowlisted preimage; refusing patch")


def patch_native(data):
    m = manifest("native")
    require(data, m["original_sha256"])
    result = bytearray(data)
    for edit in m["changes"]:
        offset = int(edit["file_offset"], 16)
        old, new = bytes.fromhex(edit["original_hex"]), bytes.fromhex(
            edit["patched_hex"]
        )
        if len(old) != len(new) or data[offset : offset + len(old)] != old:
            raise ValueError("native range preimage mismatch")
        result[offset : offset + len(old)] = new
    require(result, m["patched_sha256"])
    return bytes(result)


def rename_module(data):
    m = manifest("ui")
    require(data, m["original_sha256"])
    result, position = bytearray(), 0
    for edit in m["rename_edits"]:
        offset, count = edit["offset"], edit["remove"]
        if offset < position or offset + count > len(data):
            raise ValueError("overlapping rename recipe")
        require(data[offset : offset + count], edit["preimage_sha256"])
        result.extend(data[position:offset])
        result.extend(bytes.fromhex(edit["new_hex"]))
        position = offset + count
    result.extend(data[position:])
    require(result, m["renamed_sha256"])
    return bytes(result)


def compile_wrapper(qjsc):
    m = manifest("ui")
    compiler = Path(qjsc).resolve()
    require(compiler.read_bytes(), m["qjsc_sha256"])
    source = ROOT / "templates" / WRAPPER.removesuffix(".bin")
    require(source.read_bytes(), m["wrapper_source_sha256"])
    # Caller must supply their licensed, exact compatible compiler. It compiles
    # our own source; no proprietary bytecode is executed.
    with tempfile.TemporaryDirectory(prefix="littlep-wrapper-") as td:
        out = Path(td) / "wrapper.c"
        subprocess.run(
            [
                str(compiler),
                "-m",
                "-M",
                RENAMED.removesuffix(".bin"),
                "-M",
                "VerticalScroller-8d348f67.js",
                "-N",
                "littlep_custom_robot",
                "-o",
                str(out),
                "-c",
                source.name,
            ],
            cwd=source.parent,
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        text = out.read_text("utf-8")
        match = re.search(
            r"const uint8_t littlep_custom_robot\[\d+\] = \{([^}]+)\}", text, re.S
        )
        if not match:
            raise ValueError("compiler output format unsupported")
        data = bytes(int(x, 16) for x in re.findall(r"0x([0-9a-fA-F]{2})", match[1]))
    require(data, m["wrapper_sha256"])
    return data


def prepare(app, qjsc=None):
    app = Path(app)
    m = manifest("ui")
    native = patch_native((app / NATIVE).read_bytes())
    renamed = rename_module((app / WRAPPER).read_bytes())
    raw = (app / "manifest.json").read_bytes()
    require(raw, m["manifest_original_sha256"])
    meta = json.loads(raw)
    if meta.get("appid") != "8001707294117702" or meta.get("version") != "2.3.6":
        raise ValueError("unsupported Little P application")
    if not qjsc:
        return {NATIVE: native, RENAMED: renamed}
    wrapper = compile_wrapper(qjsc)
    files = {NATIVE: native, WRAPPER: wrapper, RENAMED: renamed}
    for name, data in files.items():
        if name == NATIVE:
            continue
        # Only extend/update local app checksum entries; leave other metadata.
        meta["cert"][name] = {"size": len(data), "md5": hashlib.md5(data).hexdigest()}
    files["manifest.json"] = (
        json.dumps(meta, ensure_ascii=False, indent=2) + "\n"
    ).encode()
    require(files["manifest.json"], m["manifest_patched_sha256"])
    return files


def write_payload(app, out, files):
    out = Path(out)
    if out.exists():
        raise ValueError("output directory already exists")
    # This output is private, derived from the caller-owned app. Never commit it.
    out.mkdir(parents=True)
    originals, patched = [], []
    for name, data in files.items():
        target = out / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        old = Path(app) / name
        if old.exists():
            originals.append(f"{digest(old.read_bytes())}  {name}")
        patched.append(f"{digest(data)}  {name}")
    (out / "original.sha256").write_text("\n".join(originals) + "\n", encoding="utf-8")
    (out / "patched.sha256").write_text("\n".join(patched) + "\n", encoding="utf-8")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument(
        "--app",
        required=True,
        help="private original app directory pulled from your device",
    )
    p.add_argument("--qjsc", help="exact compatible, user-supplied qjsc compiler")
    p.add_argument("--output", help="private generated payload directory")
    p.add_argument("--dry-run", action="store_true")
    a = p.parse_args()
    files = prepare(a.app, a.qjsc)
    if not a.dry_run:
        if not a.output or not a.qjsc:
            p.error("--output and --qjsc required for full payload")
        write_payload(a.app, a.output, files)
    print(
        json.dumps(
            {
                "preimages_verified": True,
                "full_ui_compilation": bool(a.qjsc),
                "dry_run": a.dry_run,
                "files": {k: digest(v) for k, v in files.items()},
            },
            indent=2,
        )
    )


if __name__ == "__main__":
    main()

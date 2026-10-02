#!/usr/bin/env python3
"""Portable build/test/verify/package entry point. No device or API access."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import zipfile

ROOT = Path(__file__).resolve().parents[1]
DIST = ROOT / "dist"


def run(args, cwd=ROOT, env=None):
    subprocess.run([str(a) for a in args], cwd=cwd, env=env, check=True)


def go():
    return os.environ.get("GO", "go")


def build():
    DIST.mkdir(exist_ok=True)
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="arm64")
    for cmd in ("littlep-bridge", "littlep-supervisor"):
        run(
            [
                go(),
                "build",
                "-trimpath",
                "-buildvcs=false",
                "-ldflags=-s -w -buildid=",
                "-o",
                DIST / cmd,
                "./cmd/" + cmd,
            ],
            ROOT / "bridge",
            env,
        )
    sums = "".join(
        hashlib.sha256((DIST / n).read_bytes()).hexdigest() + "  " + n + "\n"
        for n in ("littlep-bridge", "littlep-supervisor")
    )
    (DIST / "SHA256SUMS").write_text(sums, encoding="utf-8")
    print(sums, end="")


def test():
    run([go(), "test", "./..."], ROOT / "bridge")
    run([go(), "vet", "./..."], ROOT / "bridge")
    run(
        [sys.executable, "-m", "unittest", "discover", "-s", "tests", "-p", "test_*.py"]
    )
    run(["node", "--experimental-vm-modules", "tests/test_ui.mjs"])


def source_files():
    ignored = {
        ".git",
        "dist",
        "private",
        "backup",
        "payload",
        "__pycache__",
        ".idea",
        ".vscode",
    }
    for p in sorted(ROOT.rglob("*")):
        rel = p.relative_to(ROOT)
        if (
            not p.is_file()
            or any(x in ignored for x in rel.parts)
            or p.suffix in (".pyc", ".zip", ".log")
            or rel.as_posix() == "config.json"
        ):
            continue
        yield p


def audit():
    failures = []
    rules = {
        "API-key-like": re.compile(r"\bsk-[A-Za-z0-9_-]{16,}"),
        "personal-Windows-path": re.compile(r"[A-Za-z]:[\\/]Users[\\/]", re.I),
        "MAC-like": re.compile(r"\b(?:[0-9a-f]{2}:){5}[0-9a-f]{2}\b", re.I),
        "real-authorization-value": re.compile(r"Bearer\s+[A-Za-z0-9_-]{20,}"),
    }
    for p in source_files():
        rel = p.relative_to(ROOT).as_posix()
        if (
            p.suffix.lower()
            in (".img", ".so", ".bin", ".exe", ".dll", ".jpg", ".jpeg", ".png", ".pcap")
            or p.stat().st_size > 1024 * 1024
        ):
            failures.append((rel, "unexpected binary/large file"))
            continue
        try:
            s = p.read_text("utf-8")
        except UnicodeDecodeError:
            failures.append((rel, "non-text file"))
            continue
        for name, rule in rules.items():
            if rule.search(s):
                failures.append((rel, name))
        if p.suffix == ".md":
            for link in re.findall(r"\]\(([^)]+)\)", s):
                target = link.split("#")[0]
                if not target or "://" in target or target.startswith("mailto:"):
                    continue
                if not (p.parent / target).exists():
                    failures.append((rel, "broken relative link"))
    # Values and matching lines are intentionally never printed.
    for path, category in failures:
        print("FAIL", category, path)
    if failures:
        raise SystemExit(1)
    inventory = ROOT / "SOURCE_SHA256SUMS"
    if inventory.exists():
        recorded = dict(
            line.split("  ", 1)[::-1]
            for line in inventory.read_text("utf-8").splitlines()
        )
        actual = {
            p.relative_to(ROOT).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in source_files()
            if p != inventory
        }
        if recorded != actual:
            raise SystemExit("Source checksum inventory does not match staged files")
    print("PASS source secret/binary/link scan")


def verify():
    test()
    formatter = os.environ.get(
        "GOFMT",
        (
            str(Path(go()).with_name("gofmt.exe" if os.name == "nt" else "gofmt"))
            if go() != "go"
            else "gofmt"
        ),
    )
    result = subprocess.check_output([formatter, "-l", str(ROOT / "bridge")], text=True)
    if result.strip():
        raise SystemExit("Go sources must be gofmt formatted")
    config = json.loads((ROOT / "config/config.example.json").read_text())
    if (
        config["deepseek_api_key"] != "YOUR_DEEPSEEK_API_KEY"
        or config["listen"] != "127.0.0.1:18181"
    ):
        raise SystemExit("unsafe example config")
    run([sys.executable, "patch/scripts/validate_manifests.py"])
    shell = shutil.which("sh")
    if not shell:
        raise SystemExit(
            "POSIX sh required for shell syntax checks (use WSL on Windows)"
        )
    for p in ROOT.rglob("*.sh"):
        run([shell, "-n", p])
    build()
    audit()


def package():
    audit()
    DIST.mkdir(exist_ok=True)
    out = DIST / "littlep-deepseek-source-ready.zip"
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        for p in source_files():
            info = zipfile.ZipInfo(
                "littlep-deepseek/" + p.relative_to(ROOT).as_posix(),
                date_time=(2026, 1, 1, 0, 0, 0),
            )
            info.compress_type = zipfile.ZIP_DEFLATED
            mode = 0o755 if p.suffix == ".sh" else 0o644
            info.external_attr = (0o100000 | mode) << 16
            z.writestr(info, p.read_bytes())
    h = hashlib.sha256(out.read_bytes()).hexdigest()
    (DIST / (out.name + ".sha256")).write_text(
        h + "  " + out.name + "\n", encoding="utf-8"
    )
    print(out.name, h)


def inventory():
    target = ROOT / "SOURCE_SHA256SUMS"
    records = [
        hashlib.sha256(p.read_bytes()).hexdigest()
        + "  "
        + p.relative_to(ROOT).as_posix()
        + "\n"
        for p in source_files()
        if p != target
    ]
    target.write_text("".join(records), encoding="utf-8", newline="\n")
    print("Source checksum inventory refreshed:", len(records), "files")


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument(
        "command", choices=("build", "test", "verify", "audit", "package", "inventory")
    )
    a = p.parse_args()
    globals()[a.command]()

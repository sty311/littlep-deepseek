#!/usr/bin/env python3
"""Linux-only mock supervisor acceptance; no API or device involved."""
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
if not sys.platform.startswith("linux"):
    raise SystemExit("Run lifecycle tests on Linux or WSL.")
dist = ROOT / "dist"
dist.mkdir(exist_ok=True)
go = os.environ.get("GO", "go")
env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
subprocess.run(
    [
        go,
        "build",
        "-trimpath",
        "-buildvcs=false",
        "-o",
        str(dist / "littlep-supervisor-linux-amd64"),
        "./cmd/littlep-supervisor",
    ],
    cwd=ROOT / "bridge",
    env=env,
    check=True,
)
subprocess.run(
    [
        go,
        "build",
        "-trimpath",
        "-buildvcs=false",
        "-o",
        str(dist / "fixture-bridge-linux-amd64"),
        str(ROOT / "tests/fixture_bridge.go"),
    ],
    cwd=ROOT / "bridge",
    env=env,
    check=True,
)
subprocess.run([sys.executable, str(ROOT / "tests/test_lifecycle.py")], check=True)

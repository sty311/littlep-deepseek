#!/usr/bin/env python3
"""Run on Linux/WSL after build_supervisor.py; uses only a synthetic bridge."""
from __future__ import annotations

import json
import hashlib
import os
import signal
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time

HERE = Path(__file__).resolve().parent
BUILD = HERE.parent / "dist/littlep-supervisor-linux-amd64"


def port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def until(predicate, seconds=5):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if predicate():
            return True
        time.sleep(0.05)
    return predicate()


def main():
    results = {}
    with tempfile.TemporaryDirectory(prefix="r7-lifecycle-") as td:
        root = Path(td)
        binary = root / "littlep-supervisor"
        fixture = root / "fixture-bridge"
        shutil.copy2(BUILD, binary)
        shutil.copy2(HERE.parent / "dist/fixture-bridge-linux-amd64", fixture)
        binary.chmod(0o700)
        fixture.chmod(0o700)
        p = port()
        env = dict(
            os.environ, R7_ROOT=str(root), R7_BRIDGE=str(fixture), R7_PORT=str(p)
        )

        def cmd(name, check=True, **updates):
            cp = subprocess.run(
                [str(binary), name],
                env=dict(env, **updates),
                text=True,
                capture_output=True,
                timeout=10,
            )
            if check and cp.returncode:
                raise AssertionError(f"{name}: {cp.stderr}")
            return cp

        def status():
            return json.loads(cmd("status").stdout)

        try:
            # Busy port must not be claimed or terminated.
            with socket.socket() as busy:
                busy.bind(("127.0.0.1", p))
                busy.listen()
                assert cmd("start", check=False).returncode != 0
                assert not status()["supervisor_running"]
                busy.settimeout(0.2)
            results["port_busy_refused"] = True

            # A stale identity with a live PID but wrong starttime is ignored.
            (root / "run").mkdir(exist_ok=True)
            (root / "run/r7-supervisor.json").write_text(
                json.dumps(
                    {"pid": os.getpid(), "starttime": "0", "exe": "/usr/bin/python3"}
                )
            )
            assert not status()["supervisor_running"]
            results["stale_pid_rejected"] = True

            # A live unrelated PID record must not authorize signaling.
            stranger = subprocess.Popen(["sleep", "20"])
            try:
                stat = Path(f"/proc/{stranger.pid}/stat").read_text()
                starttime = stat[stat.rfind(") ") + 2 :].split()[19]
                exe = os.readlink(f"/proc/{stranger.pid}/exe")
                (root / "run/r7-supervisor.json").write_text(
                    json.dumps(
                        {"pid": stranger.pid, "starttime": starttime, "exe": exe}
                    )
                )
                cmd("stop")
                assert stranger.poll() is None
                assert not status()["supervisor_running"]
            finally:
                stranger.terminate()
                stranger.wait(timeout=2)
            results["unrelated_live_pid_preserved"] = True
            (root / "DISABLED").unlink()

            # Parallel, repeated start remains one supervisor and one bridge.
            a = subprocess.Popen(
                [str(binary), "start"],
                env=env,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            b = subprocess.Popen(
                [str(binary), "start"],
                env=env,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            assert a.communicate(timeout=10) is not None and a.returncode == 0
            assert b.communicate(timeout=10) is not None and b.returncode == 0
            assert until(lambda: status()["bridge_healthy"])
            first = status()
            assert first["bridge_version"] == "0.7.0-source-ready"
            assert status()["supervisor_pid"] == first["supervisor_pid"]
            assert status()["bridge_pid"] == first["bridge_pid"]
            assert cmd("start").returncode == 0
            results["single_instance_concurrent_repeat"] = True

            os.kill(first["supervisor_pid"], signal.SIGTERM)
            assert until(lambda: not status()["supervisor_running"])
            assert status()["enabled"]
            cmd("start")
            assert until(lambda: status()["bridge_healthy"])
            results["shutdown_signal_preserves_enable"] = True

            cmd("stop")
            assert not status()["supervisor_running"] and not status()["bridge_running"]
            assert not status()["enabled"]
            assert cmd("start", check=False).returncode != 0
            results["stop_disables_restart"] = True

            cmd("enable")
            assert until(lambda: status()["bridge_healthy"])
            cmd("restart")
            assert until(lambda: status()["bridge_healthy"])
            results["enable_restart"] = True
            cmd("disable")

            # Crash policy: 3/5/10 seconds, crash-loop within 60 seconds.
            assert cmd("enable", check=False, R7_FIXTURE_MODE="crash").returncode != 0
            assert until(
                lambda: "restart_backoff seconds=3"
                in (root / "bridge.log").read_text(),
                3,
            )
            assert until(
                lambda: "restart_backoff seconds=5"
                in (root / "bridge.log").read_text(),
                6,
            )
            assert until(
                lambda: "crash_loop count=3 window_s=60"
                in (root / "bridge.log").read_text(),
                10,
            )
            text = (root / "bridge.log").read_text()
            assert (
                "private question" not in text and "private_question_text" not in text
            )
            assert "query=" not in text
            assert "search message_id=SYNTH latency_ms=7 ok=true count=1" in text
            cmd("disable")
            results["crash_backoff_and_privacy"] = True

            # Live rotation while child is writing, followed by clean shutdown.
            cmd("enable", R7_FIXTURE_MODE="rotate")
            assert until(lambda: status()["bridge_healthy"], 10)
            assert until(lambda: (root / "bridge.log.1").exists(), 10)
            assert (root / "bridge.log").stat().st_size <= 3 * 1024 * 1024
            assert "SECRET_SENTINEL" not in (root / "bridge.log").read_text()
            assert "SECRET_SENTINEL" not in (root / "bridge.log.1").read_text()
            assert "OVERSIZED_PRIVATE_SENTINEL" not in (root / "bridge.log").read_text()
            assert (
                "OVERSIZED_PRIVATE_SENTINEL" not in (root / "bridge.log.1").read_text()
            )
            cmd("stop")
            results["rotation_live"] = True
            results["final_status"] = status()
        finally:
            cmd("disable", check=False)
    results["ok"] = all(v is True for k, v in results.items() if k != "final_status")
    print(json.dumps(results, indent=2))


if __name__ == "__main__":
    main()

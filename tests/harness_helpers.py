"""Shared bits for acceptance tests that run the real CLI against a real terminal."""
import os
import sys
import time
from pathlib import Path

FAKE = str(Path(__file__).with_name("fake_harness.py"))


def wait_for(fn, timeout=20.0, what="condition"):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        v = fn()
        if v:
            return v
        time.sleep(0.1)
    raise AssertionError(f"timed out waiting for {what}")


def read_log(path):
    return Path(path).read_text().splitlines() if Path(path).exists() else []


def harness_ops_cmd():
    return [sys.executable, "-m", "termctl.harness.cli"]


def base_env(extra):
    env = {k: v for k, v in os.environ.items() if k not in ("TMUX", "TMUX_PANE", "ITERM_SESSION_ID", "REX_SESSION", "REX_BLOCK")}
    env.update(extra)
    return env

"""Opt-in iTerm2 acceptance test (TERMCTL_ITERM_TEST=1). Opens and closes one scratch tab.

Sends text only to the tab it created.
"""
import os
import subprocess
import sys

import pytest

from tests.harness_helpers import FAKE, base_env, harness_ops_cmd, read_log, wait_for

pytestmark = pytest.mark.skipif(os.environ.get("TERMCTL_ITERM_TEST") != "1", reason="set TERMCTL_ITERM_TEST=1")


def test_clear_first_on_iterm2(tmp_path):
    from termctl.backends.iterm2_backend import ITerm2Backend

    env = base_env({"ITERM_SESSION_ID": os.environ["ITERM_SESSION_ID"], "ITERM2_COOKIE": os.environ.get("ITERM2_COOKIE", "")})
    if not env["ITERM2_COOKIE"]:
        del env["ITERM2_COOKIE"]
    b = ITerm2Backend()
    log = tmp_path / "harness.log"
    pane = b.spawn("tab", f"{sys.executable} {FAKE} {log}", cwd=str(tmp_path), title="termctl-iterm-test")
    try:
        b.set_label(pane, "termctl-iterm-test")
        wait_for(lambda: any(l.startswith("> ") or l == ">" for l in b.capture(pane, 40)), what="fake prompt")
        r = subprocess.run(
            [*harness_ops_cmd(), "--backend", "iterm2", "msg", "--clear-first", "termctl-iterm-test", "start", "over"],
            capture_output=True, text=True, env=env, timeout=60,
        )
        assert r.returncode == 0, r.stdout + r.stderr
        wait_for(lambda: len(read_log(log)) >= 2, what="two log lines")
        lines = read_log(log)
        print(lines)
        assert lines[0] == "/clear"
        assert lines[1].startswith("[agent-msg from ") and lines[1].endswith("] start over")
        assert len(lines) == 2
    finally:
        b.close(pane)
    assert all(p.id != pane.id for p in b.list_panes())

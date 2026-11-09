"""The agent-msg failure case: `/clear` then a message must be two separate lines."""
import shutil
import subprocess

import pytest

from tests.harness_helpers import FAKE, base_env, harness_ops_cmd, read_log, wait_for

SOCKET = "termctl-test"
pytestmark = pytest.mark.skipif(not shutil.which("tmux"), reason="tmux not installed")


def tmux(*a):
    return subprocess.run(["tmux", "-L", SOCKET, *a], capture_output=True, text=True, check=True).stdout.strip()


@pytest.fixture
def rig(tmp_path):
    subprocess.run(["tmux", "-L", SOCKET, "kill-server"], capture_output=True)
    tmux("new-session", "-d", "-s", "base", "-x", "200", "-y", "50")
    log = tmp_path / "harness.log"
    import sys as _s
    harness_id = tmux("new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "fakeh", "--", f"{_s.executable} {FAKE} {log}")
    sender_id = tmux("new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "sender", "--", "sleep 300")
    socket_path = tmux("display-message", "-p", "#{socket_path}")
    wait_for(lambda: ">" in tmux("capture-pane", "-p", "-t", harness_id).split(), what="fake prompt")
    env = base_env({"TMUX": f"{socket_path},1,0", "TMUX_PANE": sender_id})
    tmux("set-option", "-p", "-t", harness_id, "@termctl_label", "fakeh")
    tmux("set-option", "-p", "-t", sender_id, "@termctl_label", "sender")
    yield {"log": log, "env": env, "harness": harness_id, "sender": sender_id}
    subprocess.run(["tmux", "-L", SOCKET, "kill-server"], capture_output=True)


def ops(rig, *argv, timeout=60):
    return subprocess.run(
        [*harness_ops_cmd(), "--backend", "tmux", "-L", SOCKET, *argv],
        capture_output=True, text=True, env=rig["env"], timeout=timeout,
    )


def test_clear_first_produces_two_separate_lines(rig):
    r = ops(rig, "msg", "--clear-first", "fakeh", "please", "start", "over")
    assert r.returncode == 0, r.stdout + r.stderr
    wait_for(lambda: len(read_log(rig["log"])) >= 2, what="two log lines")
    lines = read_log(rig["log"])
    print(lines)
    assert lines[0] == "/clear"
    assert lines[1].startswith("[agent-msg from sender (")
    assert lines[1].endswith("] please start over")
    assert f"id=tmux:{rig['sender']}" in lines[1]
    assert len(lines) == 2


def test_draft_refused_exit_4(rig):
    tmux("send-keys", "-l", "-t", rig["harness"], "typing a draft")
    wait_for(lambda: "typing a draft" in tmux("capture-pane", "-p", "-t", rig["harness"]), what="draft on screen")
    r = ops(rig, "msg", "fakeh", "hello")
    assert r.returncode == 4 and "draft" in r.stderr
    assert read_log(rig["log"]) == []


def test_slash_wait_returns_after_clear(rig):
    r = ops(rig, "slash", "--wait", "fakeh", "/clear")
    assert r.returncode == 0, r.stderr
    assert read_log(rig["log"]) == ["/clear"]
    assert "<<CLEARED>>" in tmux("capture-pane", "-p", "-t", rig["harness"])


def test_self_refused(rig):
    r = ops(rig, "msg", "sender", "hi")
    assert r.returncode == 2 and "own pane" in r.stderr
    assert read_log(rig["log"]) == []


def test_spawn_generic_submits_first_prompt(rig, tmp_path):
    import sys as _s
    log2 = tmp_path / "spawned.log"
    r = ops(rig, "spawn", "generic", "--where", "tab", "--cwd", str(tmp_path),
            "--command", f"{_s.executable} {FAKE} {log2}", "--label", "worker-2",
            "--prompt", "first prompt")
    assert r.returncode == 0, r.stdout + r.stderr
    wait_for(lambda: read_log(log2), what="first prompt")
    assert read_log(log2) == ["first prompt"]
    r2 = ops(rig, "which", "worker-2")
    assert r2.returncode == 0 and "worker-2" in r2.stdout

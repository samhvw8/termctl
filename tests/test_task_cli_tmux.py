"""Tasks end to end through the real CLI, on a private tmux socket, with the fake harness."""
import json
import shutil
import subprocess
import sys

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
    owner_log = tmp_path / "owner.log"
    owner = tmux("new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "owner", "--", f"{sys.executable} {FAKE} {owner_log}")
    tmux("set-option", "-p", "-t", owner, "@termctl_label", "owner")
    wait_for(lambda: ">" in tmux("capture-pane", "-p", "-t", owner).split(), what="owner prompt")
    socket_path = tmux("display-message", "-p", "#{socket_path}")
    yield {"tmp": tmp_path, "owner": owner, "owner_log": owner_log, "socket_path": socket_path}
    subprocess.run(["tmux", "-L", SOCKET, "kill-server"], capture_output=True)


def ops(rig, pane, *argv, timeout=60):
    env = base_env({"TMUX": f"{rig['socket_path']},1,0", "TMUX_PANE": pane})
    return subprocess.run([*harness_ops_cmd(), "--backend", "tmux", "-L", SOCKET, *argv],
                          capture_output=True, text=True, env=env, timeout=timeout)


def new_task(rig, brief="write the report"):
    worker_log = rig["tmp"] / "worker.log"
    r = ops(rig, rig["owner"], "task", "new", "generic", "--title", "report", "--brief", brief,
            "--cwd", str(rig["tmp"]), "--command", f"{sys.executable} {FAKE} {worker_log}", "--timeout", "30")
    assert r.returncode == 0, r.stdout + r.stderr
    task_id = r.stdout.strip().splitlines()[-1]
    shown = ops(rig, rig["owner"], "task", "show", task_id, "--json")
    assert shown.returncode == 0, shown.stderr
    return task_id, json.loads(shown.stdout), worker_log


def test_task_new_done_notify_close(rig):
    task_id, task, worker_log = new_task(rig)
    wait_for(lambda: read_log(worker_log), what="brief in worker")
    [line] = read_log(worker_log)
    print(line)
    assert f"[task {task_id} from owner] write the report" in line and f"task done {task_id}" in line
    worker = task["pane"].split(":", 1)[1]

    r = ops(rig, rig["owner"], "task", "close", task_id)
    assert r.returncode == 6, r.stderr  # still running

    r = ops(rig, worker, "task", "done", task_id, "--status", "ok", "--summary", "report written")
    assert r.returncode == 0, r.stdout + r.stderr
    wait_for(lambda: read_log(rig["owner_log"]), what="owner notified")
    [note] = read_log(rig["owner_log"])
    print(note)
    assert note.startswith("[agent-msg from ") and f"RE: task {task_id}" in note and "report written" in note

    listed = ops(rig, rig["owner"], "task", "ls")
    assert task_id in listed.stdout and "done" in listed.stdout and "report written" in listed.stdout

    r = ops(rig, rig["owner"], "task", "close", task_id)
    assert r.returncode == 0, r.stderr
    assert worker not in tmux("list-panes", "-a", "-F", "#{pane_id}").split()
    assert task_id not in ops(rig, rig["owner"], "task", "ls").stdout


def test_task_wait_any_and_lost(rig):
    task_id, task, _ = new_task(rig)
    worker = task["pane"].split(":", 1)[1]
    r = ops(rig, rig["owner"], "task", "wait", "--any", "--timeout", "1")
    assert r.returncode == 5
    tmux("kill-pane", "-t", worker)
    r = ops(rig, rig["owner"], "task", "wait", task_id, "--timeout", "20")
    assert r.returncode == 0 and "lost" in r.stdout


def test_notification_waits_for_owner_draft(rig):
    task_id, task, _ = new_task(rig)
    worker = task["pane"].split(":", 1)[1]
    tmux("send-keys", "-l", "-t", rig["owner"], "owner draft")
    wait_for(lambda: "owner draft" in tmux("capture-pane", "-p", "-t", rig["owner"]), what="draft")
    r = ops(rig, worker, "task", "done", task_id, "--status", "ok", "--summary", "held back")
    assert r.returncode == 0 and "pending" in r.stdout, r.stdout + r.stderr
    assert read_log(rig["owner_log"]) == []
    tmux("send-keys", "-t", rig["owner"], "C-u")
    wait_for(lambda: "owner draft" not in tmux("capture-pane", "-p", "-t", rig["owner"]), what="draft gone")
    r = ops(rig, rig["owner"], "task", "ls")  # any harness-ops call delivers
    assert r.returncode == 0
    wait_for(lambda: read_log(rig["owner_log"]), what="late delivery")
    assert "held back" in read_log(rig["owner_log"])[0]


def test_close_command_safety(rig):
    r = ops(rig, rig["owner"], "close", "owner", "--force")
    assert r.returncode == 2, r.stderr  # never self
    extra = tmux("new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "extra", "--", "sleep 60")
    tmux("set-option", "-p", "-t", extra, "@termctl_label", "extra")
    r = ops(rig, rig["owner"], "close", "extra")
    assert r.returncode == 6, r.stderr  # not created through harness-ops
    r = ops(rig, rig["owner"], "close", "--force", "extra")
    assert r.returncode == 0, r.stderr
    assert extra not in tmux("list-panes", "-a", "-F", "#{pane_id}").split()


def test_spawn_then_close(rig):
    log = rig["tmp"] / "s.log"
    r = ops(rig, rig["owner"], "spawn", "generic", "--where", "tab", "--cwd", str(rig["tmp"]),
            "--command", f"{sys.executable} {FAKE} {log}", "--label", "sp1")
    assert r.returncode == 0, r.stdout + r.stderr
    r = ops(rig, rig["owner"], "close", "sp1")
    assert r.returncode == 0, r.stderr

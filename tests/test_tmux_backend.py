"""tmux integration on a private socket: never touches the user's server."""
import shutil
import subprocess

import pytest

from termctl.backends.tmux import TmuxBackend
from termctl.errors import BackendError, UsageError

SOCKET = "termctl-test"
pytestmark = pytest.mark.skipif(not shutil.which("tmux"), reason="tmux not installed")


def tmux(*a):
    return subprocess.run(["tmux", "-L", SOCKET, *a], capture_output=True, text=True)


@pytest.fixture
def backend():
    tmux("kill-server")
    r = tmux("new-session", "-d", "-s", "base", "-x", "160", "-y", "40")
    assert r.returncode == 0, r.stderr
    yield TmuxBackend(socket=SOCKET)
    tmux("kill-server")


def test_spawn_send_capture_close(backend, tmp_path):
    p = backend.spawn("tab", "cat", cwd=str(tmp_path), title="cat-pane")
    assert p.id.startswith("tmux:%")
    backend.send_text(p, "hello-literal $HOME")
    backend.send_key(p, "enter")
    for _ in range(50):
        lines = backend.capture(p, 20)
        if lines.count("hello-literal $HOME") >= 2:
            break
        subprocess.run(["sleep", "0.05"])
    assert lines.count("hello-literal $HOME") == 2  # terminal echo and cat's output
    backend.close(p)
    assert all(x.id != p.id for x in backend.list_panes())


def test_list_label_title_folder(backend, tmp_path):
    p = backend.spawn("tab", "sleep 30", cwd=str(tmp_path), title="sleeper")
    backend.set_label(p, "worker-1")
    panes = {x.id: x for x in backend.list_panes()}
    got = panes[p.id]
    assert got.label == "worker-1" and got.title == "sleeper"
    # macOS tempdirs resolve through /private
    assert got.folder.endswith(tmp_path.name)
    assert got.process == "sleep"


def test_split_and_window(backend):
    base = backend.list_panes()[0]
    backend.self_pane = lambda: base
    s = backend.spawn("split", "sleep 30")
    w = backend.spawn("window", "sleep 30", title="neww")
    ids = {x.id for x in backend.list_panes()}
    assert s.id in ids and w.id in ids


def test_send_text_is_literal(backend):
    """tmux treats a final ';' as a command separator; the backend must escape it."""
    p = backend.spawn("tab", "cat")
    backend.send_text(p, "Enter C-c ;")
    for _ in range(50):
        if "Enter C-c ;" in "\n".join(backend.capture(p, 10)):
            return
        subprocess.run(["sleep", "0.05"])
    pytest.fail("literal text did not arrive")


def test_send_text_keeps_a_literal_backslash_semicolon(backend):
    p = backend.spawn("tab", "cat")
    backend.send_text(p, r"path\;")
    for _ in range(50):
        if r"path\;" in "\n".join(backend.capture(p, 10)):
            return
        subprocess.run(["sleep", "0.05"])
    pytest.fail(repr(backend.capture(p, 10)))


def test_keys(backend):
    p = backend.spawn("tab", "cat")
    backend.send_key(p, "escape")
    with pytest.raises(UsageError):
        backend.send_key(p, "nonsense-key")
    backend.send_key(p, "ctrl-c")  # ends cat, so the pane goes away
    for _ in range(50):
        if all(x.id != p.id for x in backend.list_panes()):
            return
        subprocess.run(["sleep", "0.05"])
    pytest.fail("ctrl-c did not end cat")


def test_missing_server_is_a_backend_error():
    b = TmuxBackend(socket="termctl-test-none")
    subprocess.run(["tmux", "-L", "termctl-test-none", "kill-server"], capture_output=True)
    assert b.available() is False
    with pytest.raises(BackendError):
        b.capture(type("P", (), {"native": "%0", "id": "tmux:%0"})(), 5)


def test_no_server_means_no_panes():
    b = TmuxBackend(socket="termctl-test-none")
    subprocess.run(["tmux", "-L", "termctl-test-none", "kill-server"], capture_output=True)
    assert b.list_panes() == []


def test_server_ref_reopens_the_same_server(backend):
    p = backend.spawn("tab", "sleep 30", title="reach")
    ref = backend.server_ref()
    assert ref["socket_path"] and ref["pid"]
    again = TmuxBackend(socket_path=ref["socket_path"])
    assert p.id in {x.id for x in again.list_panes()}
    assert again.server_ref() == ref

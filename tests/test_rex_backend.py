"""Rex argv mapping against a stub; a live check runs only when `rex` is installed."""
import json
import shutil
import subprocess

import pytest

from termctl.backends.rex import RexBackend
from termctl.errors import BackendError, TargetError
from termctl.pane import Pane


class Stub:
    def __init__(self):
        self.calls = []
        self.replies = []

    def __call__(self, argv, **kw):
        self.calls.append(argv)
        out, code = self.replies.pop(0) if self.replies else ("", 0)
        return subprocess.CompletedProcess(argv, code, stdout=out, stderr="ambiguous" if code == 3 else "boom")


@pytest.fixture
def stub(monkeypatch):
    s = Stub()
    monkeypatch.setattr("termctl.backends.rex.subprocess.run", s)
    return s


def pane(id="abcd1234"):
    return Pane(id=f"rex:{id}", backend="rex", native=id)


def test_list_panes_reads_blocks(stub):
    stub.replies = [(json.dumps([{"id": "b1", "label": "logs", "title": "t", "cwd": "/w", "session": "s1"}]), 0)]
    panes = RexBackend().list_panes()
    assert stub.calls[0][:3] == ["rex", "block", "ls"] and "-A" in stub.calls[0] and "--json" in stub.calls[0]
    assert panes[0].id == "rex:b1" and panes[0].label == "logs" and panes[0].folder == "/w"


def test_send_text(stub):
    RexBackend().send_text(pane(), "hello")
    assert stub.calls[0] == ["rex", "send", "-b", "abcd1234", "hello"]


def test_send_key_maps_names(stub):
    b = RexBackend()
    b.send_key(pane(), "enter")
    b.send_key(pane(), "ctrl-u")
    assert stub.calls[0] == ["rex", "send-key", "-b", "abcd1234", "enter"]
    assert stub.calls[1] == ["rex", "send-key", "-b", "abcd1234", "ctrl+u"]


def test_capture(stub):
    stub.replies = [("line1\nline2\n", 0)]
    assert RexBackend().capture(pane(), 1) == ["line2"]
    assert stub.calls[0] == ["rex", "capture", "-b", "abcd1234", "--trim"]


def test_spawn_tab_does_not_steal_focus(stub, monkeypatch):
    monkeypatch.setenv("REX_SESSION", "s1")
    stub.replies = [(json.dumps({"id": "b2"}), 0)]
    p = RexBackend().spawn("tab", "omp", cwd="/w", title="t")
    argv = stub.calls[0]
    assert argv[:3] == ["rex", "window", "new"] and "--focus=false" in argv and "--json" in argv
    assert p.id == "rex:b2"


def test_close_and_label(stub):
    b = RexBackend()
    b.close(pane())
    b.set_label(pane(), "worker")
    assert stub.calls[0] == ["rex", "block", "close", "abcd1234"]
    assert stub.calls[1] == ["rex", "block", "rename", "abcd1234", "worker"]


def test_exit_3_is_a_target_error(stub):
    stub.replies = [("", 3)]
    with pytest.raises(TargetError) as e:
        RexBackend().send_text(pane(), "x")
    assert e.value.code == 2


def test_other_failure_is_a_backend_error(stub):
    stub.replies = [("", 1)]
    with pytest.raises(BackendError):
        RexBackend().send_text(pane(), "x")


@pytest.mark.skipif(not shutil.which("rex"), reason="rex not installed")
def test_live_rex_lists():
    assert isinstance(RexBackend().list_panes(), list)

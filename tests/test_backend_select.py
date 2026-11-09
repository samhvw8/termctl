import pytest

from termctl.backends import detect, make
from termctl.backends.base import Backend, check_text
from termctl.errors import BackendError, UsageError


def test_detect_priority():
    env = {"ITERM_SESSION_ID": "w0:ABC", "TMUX": "/tmp/s,1,0", "REX_SESSION": "r"}
    assert detect(env, iterm_installed=False) == "tmux"
    del env["TMUX"]
    assert detect(env, iterm_installed=False) == "rex"
    del env["REX_SESSION"]
    assert detect(env, iterm_installed=False) == "iterm2"


def test_detect_nothing_without_iterm():
    assert detect({}, iterm_installed=False) is None


def test_detect_falls_back_to_iterm_when_installed():
    assert detect({}, iterm_installed=True) == "iterm2"


def test_ghostty_stub_says_why():
    b = make("ghostty")
    with pytest.raises(BackendError) as e:
        b.list_panes()
    assert "not supported" in str(e.value) and "Ghostty" in str(e.value) and e.value.code == 1


def test_unknown_backend():
    with pytest.raises(UsageError):
        make("nope")


def test_send_text_rejects_line_breaks():
    with pytest.raises(UsageError):
        check_text("a\nb")
    with pytest.raises(UsageError):
        check_text("a\rb")
    check_text("plain")


def test_backend_is_abstract():
    with pytest.raises(TypeError):
        Backend()

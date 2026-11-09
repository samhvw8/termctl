"""CLI behaviour: argument parsing, exit codes, agent-msg compatibility."""
import json

import pytest

from termctl.harness import cli as hcli
from tests.fakes import FakeBackend, make_pane


@pytest.fixture
def env(monkeypatch):
    target = make_pane(1, title="target", folder="/w/t", process="claude")
    dup_a = make_pane(2, title="dup", folder="/w/a")
    dup_b = make_pane(3, title="dup", folder="/w/b")
    me = make_pane(9, label="me", folder="/w/me", process="claude")
    target.is_self = False
    me.is_self = True
    b = FakeBackend([target, dup_a, dup_b, me], me=me)
    monkeypatch.setattr(hcli, "open_backend", lambda args, target=None: b)
    monkeypatch.setattr(hcli, "open_sender", lambda args, backend: me)
    monkeypatch.setattr(hcli.time, "sleep", lambda s: None)
    # read_input needs a claude-shaped screen for the claude target; use generic for the test pane
    target.process = "python3"
    return b


def run(argv, capsys):
    code = hcli.main(argv)
    out = capsys.readouterr()
    return code, out.out, out.err


def test_send_alias_matches_msg(env, capsys):
    code, out, _ = run(["send", "target", "hello", "world"], capsys)
    assert code == 0 and "sent to" in out
    assert env.inputs_sent("fake:1")[0][2].endswith(" hello world")


def test_msg_text_may_look_like_flags(env, capsys):
    code, _, _ = run(["msg", "target", "--", "--force", "is", "text"], capsys)
    assert code == 0
    assert env.inputs_sent("fake:1")[0][2].endswith(" --force is text")


def test_double_dash_mid_message_stays_in_text(env, capsys):
    code, _, _ = run(["msg", "target", "a", "--", "b"], capsys)
    assert code == 0
    assert env.inputs_sent("fake:1")[0][2].endswith(" a -- b")


def test_flag_after_target_is_refused(env, capsys):
    code, _, err = run(["msg", "target", "hi", "--clear-first"], capsys)
    assert code == 2 and "before the target" in err


def test_ambiguous_exit_2(env, capsys):
    code, _, err = run(["msg", "dup", "x"], capsys)
    assert code == 2 and "matches 2" in err
    assert env.inputs_sent() == []


def test_self_exit_2(env, capsys):
    code, _, err = run(["msg", "me", "x"], capsys)
    assert code == 2 and "own pane" in err


def test_draft_exit_4(env, capsys):
    env.state["fake:1"].input = "draft"
    code, _, err = run(["msg", "target", "x"], capsys)
    assert code == 4 and "draft" in err


def test_force_flag(env, capsys):
    env.state["fake:1"].input = "draft"
    code, _, _ = run(["msg", "--force", "target", "x"], capsys)
    assert code == 0


def test_clear_first_flag(env, capsys):
    code, _, _ = run(["msg", "--clear-first", "target", "x"], capsys)
    assert code == 0
    assert env.inputs_sent("fake:1")[0][2] == "/clear"


def test_clear_command(env, capsys):
    code, _, _ = run(["clear", "target"], capsys)
    assert code == 0 and env.inputs_sent("fake:1")[0][2] == "/clear"


def test_slash_command(env, capsys):
    code, _, _ = run(["slash", "target", "/compact"], capsys)
    assert code == 0 and env.inputs_sent("fake:1")[0][2] == "/compact"


def test_ls_json_has_old_fields(env, capsys):
    code, out, _ = run(["ls", "--json"], capsys)
    rows = json.loads(out)
    assert code == 0
    for key in ("id", "harness", "title", "label", "folder", "self", "status"):
        assert key in rows[0], key
    assert any(r["self"] for r in rows)


def test_which_prints_row(env, capsys):
    code, out, _ = run(["which", "target"], capsys)
    assert code == 0 and "target" in out


def test_read_prints_header_and_lines(env, capsys):
    code, out, _ = run(["read", "target", "-n", "5"], capsys)
    assert code == 0 and "---" in out and "some earlier output" in out


def test_name_validates_label(env, capsys):
    code, _, _ = run(["name", "bad label!"], capsys)
    assert code == 2
    code, out, _ = run(["name", "good-label"], capsys)
    assert code == 0 and "good-label" in out


def test_wait_idle(env, capsys):
    code, out, _ = run(["wait-idle", "target", "--timeout", "10"], capsys)
    assert code == 0 and "idle" in out


def test_slash_flag_after_the_command_is_refused(env, capsys):
    # `slash target /help --wait` must not type "--wait" into the harness.
    code, _, err = run(["slash", "target", "/help", "--wait"], capsys)
    assert code == 2 and "before the target" in err
    assert env.inputs_sent("fake:1") == []


def test_slash_keeps_its_own_arguments(env, capsys):
    code, _, _ = run(["slash", "target", "/compact", "focus", "on", "tests"], capsys)
    assert code == 0
    assert env.inputs_sent("fake:1")[0][2] == "/compact focus on tests"


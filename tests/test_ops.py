import pytest

from termctl.errors import DraftError, TargetError, UsageError, WaitTimeout
from termctl.harness.ops import Ops
from tests.fakes import FakeBackend, FakeClock, make_pane

TAG = "[agent-msg from me-label (python3) id=fake:9 folder=~/w/me]"


def setup(inputs=None, **kw):
    target = make_pane(1, title="target", folder="~/w/t")
    other = make_pane(2, title="other", folder="~/w/o")
    me = make_pane(9, label="me-label", folder="~/w/me")
    backend = FakeBackend([target, other, me], me=me, inputs=inputs, **kw)
    clock = FakeClock()
    return Ops(backend, sender=me, clock=clock.now, sleep=clock.sleep, inbox=kw.get("inbox")), backend, target, other, me


def test_msg_sends_tagged_line_then_enter():
    ops, b, t, _, _ = setup()
    ops.msg(t, "hello there")
    assert b.inputs_sent(t.id) == [("text", t.id, f"{TAG} hello there", 0), ("key", t.id, "enter", 0)]


def test_msg_refuses_when_input_line_holds_a_draft():
    ops, b, t, _, _ = setup(inputs={"fake:1": "half typed"})
    with pytest.raises(DraftError) as e:
        ops.msg(t, "hello")
    assert e.value.code == 4 and "half typed" in str(e.value)
    assert b.inputs_sent(t.id) == []


def test_msg_force_ignores_draft():
    ops, b, t, _, _ = setup(inputs={"fake:1": "half typed"})
    ops.msg(t, "hello", force=True)
    assert [e[0] for e in b.inputs_sent(t.id)] == ["text", "key"]


def test_long_message_goes_to_file(tmp_path):
    ops, b, t, _, _ = setup()
    ops.inbox = tmp_path
    body = "x" * 801
    ops.msg(t, body)
    typed = b.inputs_sent(t.id)[0][2]
    assert typed.startswith(TAG + " Long message; read the file ")
    path = typed.split("read the file ")[1]
    content = open(path).read()
    assert content.startswith(TAG + "\n\n") and body in content


def test_multiline_message_goes_to_file(tmp_path):
    ops, b, t, _, _ = setup()
    ops.inbox = tmp_path
    ops.msg(t, "line one\nline two")
    typed = b.inputs_sent(t.id)[0][2]
    assert "Long message; read the file" in typed and "\n" not in typed


def test_message_at_limit_stays_inline(tmp_path):
    ops, b, t, _, _ = setup()
    ops.inbox = tmp_path
    ops.msg(t, "y" * 800)
    assert "Long message" not in b.inputs_sent(t.id)[0][2]


def test_message_is_sanitised():
    ops, b, t, _, _ = setup()
    ops.msg(t, "a\rb\x1bc")
    assert b.inputs_sent(t.id)[0][2] == f"{TAG} a bc"


def test_empty_message_refused():
    ops, _, t, _, _ = setup()
    with pytest.raises(UsageError):
        ops.msg(t, "   ")


def test_clear_first_sends_clear_alone_waits_idle_then_message():
    ops, b, t, _, _ = setup()
    ops.msg(t, "after clear", clear_first=True)
    sent = b.inputs_sent(t.id)
    assert [(e[0], e[2]) for e in sent] == [
        ("text", "/clear"),
        ("key", "enter"),
        ("text", f"{TAG} after clear"),
        ("key", "enter"),
    ]
    # the message was typed only after the harness stopped being busy
    assert sent[2][3] == 0
    # the wait really polled the screen between /clear's Enter and the message
    first_enter = b.events.index(("key", t.id, "enter", 0))
    second_text = b.events.index(sent[2])
    assert any(e == ("capture", t.id) for e in b.events[first_enter:second_text])


def test_clear_first_refuses_on_draft_before_sending_anything():
    ops, b, t, _, _ = setup(inputs={"fake:1": "draft"})
    with pytest.raises(DraftError):
        ops.msg(t, "x", clear_first=True)
    assert b.inputs_sent(t.id) == []


def test_clear_first_force_wipes_draft_with_ctrl_u_before_clear():
    ops, b, t, _, _ = setup(inputs={"fake:1": "draft"})
    ops.msg(t, "x", clear_first=True, force=True)
    sent = b.inputs_sent(t.id)
    assert sent[0][:3] == ("key", t.id, "ctrl-u")
    assert sent[1][2] == "/clear"


def test_clear_first_refuses_message_when_slash_stays_in_input():
    """Enter was ignored: /clear is still typed. Appending would recreate the original bug."""
    ops, b, t, _, _ = setup()
    b.keep_clear_text = True
    with pytest.raises(DraftError) as e:
        ops.msg(t, "x", clear_first=True)
    assert "/clear" in str(e.value)
    assert [e_[2] for e_ in b.inputs_sent(t.id) if e_[0] == "text"] == ["/clear"]


def test_slash_alone():
    ops, b, t, _, _ = setup()
    ops.slash(t, "/compact")
    assert [(e[0], e[2]) for e in b.inputs_sent(t.id)] == [("text", "/compact"), ("key", "enter")]


def test_slash_requires_leading_slash():
    ops, b, t, _, _ = setup()
    with pytest.raises(UsageError):
        ops.slash(t, "clear")


def test_slash_rejects_newline():
    ops, b, t, _, _ = setup()
    with pytest.raises(UsageError):
        ops.slash(t, "/foo\nbar")


def test_slash_draft_refusal():
    ops, b, t, _, _ = setup(inputs={"fake:1": "draft"})
    with pytest.raises(DraftError):
        ops.slash(t, "/compact")
    assert b.inputs_sent(t.id) == []


def test_clear_waits_for_idle():
    ops, b, t, _, _ = setup()
    ops.clear(t)
    assert b.state[t.id].busy == 0
    assert ("key", t.id, "enter", 0) in b.events


def test_wait_idle_times_out():
    ops, b, t, _, _ = setup()
    b.state[t.id].busy = 10**9
    with pytest.raises(WaitTimeout) as e:
        ops.wait_idle(t, timeout=5)
    assert e.value.code == 5


def test_wait_idle_needs_stable_screen():
    ops, b, t, _, _ = setup()
    # screen keeps changing for a while, prompt visible: not idle until it stops
    st = b.state[t.id]
    calls = {"n": 0}
    orig = b.capture

    def flaky(pane, lines=200):
        calls["n"] += 1
        if calls["n"] < 5:
            st.log.append(f"output {calls['n']}")
        return orig(pane, lines)

    b.capture = flaky
    ops.wait_idle(t, timeout=60)
    assert calls["n"] >= 5


def test_msg_needs_sender():
    target = make_pane(1, title="t")
    b = FakeBackend([target])
    clock = FakeClock()
    ops = Ops(b, sender=None, clock=clock.now, sleep=clock.sleep)
    with pytest.raises(UsageError):
        ops.msg(target, "hi")


def test_self_target_refused():
    ops, b, t, _, me = setup()
    with pytest.raises(TargetError) as e:
        ops.pick("me-label")
    assert e.value.code == 2 and "own pane" in str(e.value)
    assert ops.pick("target").id == t.id


def test_ambiguous_target_refused():
    target = make_pane(1, title="dup")
    dup = make_pane(2, title="dup")
    me = make_pane(9, label="me")
    b = FakeBackend([target, dup, me], me=me)
    clock = FakeClock()
    ops = Ops(b, sender=me, clock=clock.now, sleep=clock.sleep)
    with pytest.raises(TargetError) as e:
        ops.pick("dup")
    assert e.value.code == 2


def test_spawn_waits_ready_then_submits_prompt():
    ops, b, t, _, _ = setup()
    p = ops.spawn("generic", where="tab", cwd="/tmp/x", prompt="do the thing", label="worker", command="fake-cmd --flag")
    assert b.events[0][:4] == ("spawn", "tab", "fake-cmd --flag", "/tmp/x")
    assert ("label", p.id, "worker") in b.events
    assert [(e[0], e[2]) for e in b.inputs_sent(p.id)] == [("text", "do the thing"), ("key", "enter")]


def test_spawn_multiline_prompt_goes_to_file(tmp_path):
    ops, b, t, _, _ = setup()
    ops.inbox = tmp_path
    p = ops.spawn("generic", where="tab", cwd="/tmp/x", prompt="a\nb", command="c")
    typed = b.inputs_sent(p.id)[0][2]
    assert "read the file" in typed.lower() and "\n" not in typed


def test_spawn_unknown_harness_needs_command():
    ops, *_ = setup()
    with pytest.raises(UsageError):
        ops.spawn("nonsense", where="tab", cwd="/tmp/x")


def test_spawn_rejects_both_prompt_forms():
    ops, *_ = setup()
    with pytest.raises(UsageError):
        ops.spawn("generic", where="tab", cwd="/tmp", prompt="a", prompt_file="b", command="c")

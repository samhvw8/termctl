import pytest

from termctl.errors import BackendError, DraftError, GuardError, TargetError, UsageError, WaitTimeout
from termctl.harness.ops import Ops
from termctl.harness.registry import Registry
from termctl.harness.tasks import PENDING_TTL, Tasks
from tests.fakes import FakeBackend, FakeClock, make_pane


class Rig:
    def __init__(self, tmp_path):
        self.owner = make_pane(9, label="owner", folder="~/w/o")
        self.other = make_pane(1, title="other", folder="~/w/x")
        self.backend = FakeBackend([self.other, self.owner], me=self.owner)
        self.clock = FakeClock()
        self.clock.t = 1000.0
        self.reg = Registry(tmp_path / "state")
        self.tasks = self.as_pane(self.owner)

    def as_pane(self, pane):
        return Tasks(self.reg, self.backend, sender=pane, factory=lambda pane_id, ref: self.backend,
                     clock=self.clock.now, sleep=self.clock.sleep, inbox=self.reg.root / "inbox")

    def new(self, tasks=None, **kw):
        kw.setdefault("title", "job")
        kw.setdefault("brief", "do the thing")
        kw.setdefault("cwd", "/w")
        kw.setdefault("command", "fake-cmd")
        return (tasks or self.tasks).new("generic", **kw)

    def pane(self, task):
        return next(p for p in self.backend.panes if p.id == task["pane"])

    def typed(self, pane_id):
        return [e[2] for e in self.backend.events if e[0] == "text" and e[1] == pane_id]


@pytest.fixture
def rig(tmp_path):
    return Rig(tmp_path)


def test_new_spawns_and_submits_brief_with_footer(rig):
    t = rig.new(title="write docs")
    assert t["status"] == "running" and t["owner"] == rig.owner.id and t["depth"] == 1
    assert rig.backend.events[0][:4] == ("spawn", "tab", "fake-cmd", "/w")
    [line] = rig.typed(t["pane"])
    assert line.startswith(f"[task {t['id']} from owner] do the thing")
    assert f"harness-ops task done {t['id']} --status ok|failed|blocked" in line
    assert "\n" not in line
    assert ("label", t["pane"], t["id"]) in rig.backend.events
    # the pane counts as ours, so close may act on it
    assert t["pane"] in rig.reg.read()["panes"]


def test_brief_is_stored(rig):
    t = rig.new()
    assert "do the thing" in open(t["brief"]).read()


def test_long_or_multiline_brief_goes_through_its_file(rig):
    t = rig.new(brief="line one\nline two")
    [line] = rig.typed(t["pane"])
    assert t["brief"] in line and "line two" not in line and "task done" in line
    body = open(t["brief"]).read()
    assert "line two" in body and "task done" in body


def test_new_needs_exactly_one_brief(rig):
    with pytest.raises(UsageError):
        rig.tasks.new("generic", title="x", cwd="/w", command="c")
    with pytest.raises(UsageError):
        rig.tasks.new("generic", title="x", cwd="/w", command="c", brief="a", brief_file="b")


def test_new_needs_an_owner_pane(rig):
    with pytest.raises(UsageError):
        rig.new(tasks=rig.as_pane(None))


# --- guards ------------------------------------------------------------


def test_max_running_tasks_per_owner(rig):
    for _ in range(6):
        rig.new()
    spawned = sum(1 for e in rig.backend.events if e[0] == "spawn")
    with pytest.raises(GuardError) as e:
        rig.new()
    assert e.value.code == 6 and "6" in str(e.value)
    assert sum(1 for ev in rig.backend.events if ev[0] == "spawn") == spawned


def test_max_can_be_raised_per_call_and_by_env(rig, monkeypatch):
    for _ in range(6):
        rig.new()
    rig.new(max_tasks=7)
    monkeypatch.setenv("TERMCTL_MAX_TASKS", "8")
    rig.new()


def test_finished_tasks_free_their_slot(rig):
    ts = [rig.new() for _ in range(6)]
    rig.as_pane(rig.pane(ts[0])).done(ts[0]["id"], "ok", summary="fine")
    rig.new()


def test_depth_limit(rig):
    t1 = rig.new()
    assert t1["depth"] == 1
    t2 = rig.new(tasks=rig.as_pane(rig.pane(t1)))
    assert t2["depth"] == 2
    with pytest.raises(GuardError) as e:
        rig.new(tasks=rig.as_pane(rig.pane(t2)))
    assert e.value.code == 6 and "depth" in str(e.value)


def test_depth_limit_env(rig, monkeypatch):
    monkeypatch.setenv("TERMCTL_MAX_DEPTH", "3")
    t1 = rig.new()
    t2 = rig.new(tasks=rig.as_pane(rig.pane(t1)))
    assert rig.new(tasks=rig.as_pane(rig.pane(t2)))["depth"] == 3


def test_failed_spawn_releases_the_slot(rig):
    rig.backend.fail_spawn = True
    with pytest.raises(BackendError):
        rig.new()
    rig.backend.fail_spawn = False
    [t] = rig.tasks.ls()
    assert t["status"] == "failed" and "spawn" in t["summary"]


def test_harness_not_ready_marks_failed_and_keeps_pane_closable(rig):
    orig = rig.backend.spawn

    def busy_spawn(*a, **kw):
        p = orig(*a, **kw)
        rig.backend.state[p.id].busy = 10**9
        return p

    rig.backend.spawn = busy_spawn
    with pytest.raises(WaitTimeout):
        rig.new(timeout=5)
    [t] = rig.tasks.ls()
    assert t["status"] == "failed" and t["pane"] in rig.reg.read()["panes"]


# --- status transitions ------------------------------------------------


@pytest.mark.parametrize("reported,stored", [("ok", "done"), ("failed", "failed"), ("blocked", "blocked")])
def test_done_sets_status(rig, reported, stored):
    t = rig.new()
    d = rig.as_pane(rig.pane(t)).done(t["id"], reported, summary="result text")
    assert d["status"] == stored and d["summary"] == "result text"
    assert d["ended"] == rig.clock.t or d["ended"] > 0


def test_done_twice_is_refused(rig):
    t = rig.new()
    w = rig.as_pane(rig.pane(t))
    w.done(t["id"], "ok", summary="a")
    with pytest.raises(UsageError):
        w.done(t["id"], "ok", summary="b")
    assert rig.tasks.get(t["id"])["summary"] == "a"


def test_blocked_can_finish(rig):
    t = rig.new()
    w = rig.as_pane(rig.pane(t))
    w.done(t["id"], "blocked", summary="need input")
    assert w.done(t["id"], "ok", summary="did it")["status"] == "done"


def test_done_needs_a_summary(rig):
    t = rig.new()
    with pytest.raises(UsageError):
        rig.as_pane(rig.pane(t)).done(t["id"], "ok")


def test_done_summary_file(rig, tmp_path):
    t = rig.new()
    f = tmp_path / "s.txt"
    f.write_text("from file\n")
    assert rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary_file=str(f))["summary"] == "from file"


def test_unknown_status_refused(rig):
    t = rig.new()
    with pytest.raises(UsageError):
        rig.as_pane(rig.pane(t)).done(t["id"], "maybe", summary="x")


def test_unrelated_pane_cannot_report(rig):
    t = rig.new()
    with pytest.raises(TargetError):
        rig.as_pane(rig.other).done(t["id"], "ok", summary="not mine")
    assert rig.tasks.get(t["id"])["status"] == "running"


def test_id_prefix_and_unknown_id(rig):
    t = rig.new()
    assert rig.tasks.get(t["id"][:4])["id"] == t["id"]
    with pytest.raises(TargetError):
        rig.tasks.get("zzzzzz")


def test_send_reaches_the_task_pane(rig):
    t = rig.new()
    rig.tasks.send(t["id"], "one more thing")
    assert any("one more thing" in x for x in rig.typed(t["pane"]))


def test_send_reopens_a_finished_task(rig):
    t = rig.new()
    rig.as_pane(rig.pane(t)).done(t["id"], "blocked", summary="stuck")
    rig.tasks.send(t["id"], "here is the answer")
    t2 = rig.tasks.get(t["id"])
    assert t2["status"] == "running" and t2["ended"] is None


def test_send_to_lost_task_is_refused(rig):
    t = rig.new()
    rig.backend.close(rig.pane(t))
    with pytest.raises(UsageError):
        rig.tasks.send(t["id"], "hello?")


def test_send_refuses_on_draft(rig):
    t = rig.new()
    rig.backend.state[t["pane"]].input = "half"
    with pytest.raises(DraftError):
        rig.tasks.send(t["id"], "x")


# --- lost reconciliation ------------------------------------------------


def test_gone_pane_becomes_lost_on_read(rig):
    t = rig.new()
    rig.backend.close(rig.pane(t))
    assert rig.tasks.get(t["id"])["status"] == "lost"
    assert rig.reg.read()["tasks"][t["id"]]["status"] == "lost"


def test_backend_failure_is_not_lost(rig):
    t = rig.new()
    rig.backend.fail_list = True
    assert rig.tasks.get(t["id"])["status"] == "running"


def test_restarted_server_means_lost(rig):
    t = rig.new()
    with rig.reg.update() as d:
        d["tasks"][t["id"]]["ref"] = {"pid": "111"}
    rig.backend.server_ref = lambda: {"pid": "222"}
    assert rig.tasks.get(t["id"])["status"] == "lost"


def test_finished_task_is_not_reconciled(rig):
    t = rig.new()
    rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="x")
    rig.backend.close(rig.pane(t))
    assert rig.tasks.get(t["id"])["status"] == "done"


def test_stale_reservation_becomes_lost(rig):
    with rig.reg.update() as d:
        d["tasks"]["tabc"] = {"id": "tabc", "status": "running", "pane": None, "owner": rig.owner.id,
                              "ref": {}, "started": rig.clock.t, "ended": None, "summary": "", "closed": False}
    assert rig.tasks.get("tabc")["status"] == "running"
    rig.clock.t += 601
    assert rig.tasks.get("tabc")["status"] == "lost"


# --- notifications ------------------------------------------------------


def test_done_notifies_owner_with_tagged_message(rig):
    t = rig.new()
    w = rig.pane(t)
    rig.as_pane(w).done(t["id"], "ok", summary="all good")
    [msg] = rig.typed(rig.owner.id)
    assert msg.startswith("[agent-msg from ") and f"RE: task {t['id']}" in msg and "all good" in msg
    assert rig.tasks.get(t["id"])["notify"] == "delivered"
    assert rig.reg.read()["pending"] == []


def test_draft_on_owner_keeps_notification_pending(rig):
    t = rig.new()
    rig.backend.state[rig.owner.id].input = "owner typing"
    done = rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="finished")
    assert rig.typed(rig.owner.id) == []
    assert rig.backend.state[rig.owner.id].input == "owner typing"
    assert done["notify"] == "pending" and done["status"] == "done"
    assert rig.tasks.ls()[0]["summary"] == "finished"
    assert len(rig.reg.read()["pending"]) == 1


def test_done_retries_for_a_bounded_time(rig):
    t = rig.new()
    rig.backend.state[rig.owner.id].input = "owner typing"
    start = rig.clock.t
    rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="x")
    assert 0 < rig.clock.t - start <= 15


def test_pending_is_delivered_once_the_draft_is_gone(rig):
    t = rig.new()
    st = rig.backend.state[rig.owner.id]
    st.input = "owner typing"
    rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="finished")
    st.input = ""
    assert rig.tasks.flush_pending() == 1
    assert len([m for m in rig.typed(rig.owner.id) if "finished" in m]) == 1
    assert rig.tasks.get(t["id"])["notify"] == "delivered"
    assert rig.tasks.flush_pending() == 0
    assert len([m for m in rig.typed(rig.owner.id) if "finished" in m]) == 1


def test_claimed_notification_is_not_delivered_twice(rig):
    t = rig.new()
    st = rig.backend.state[rig.owner.id]
    st.input = "owner typing"
    rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="finished")
    st.input = ""
    with rig.reg.update() as d:
        d["pending"][0]["claimed"] = rig.clock.t
    assert rig.tasks.flush_pending() == 0
    assert rig.typed(rig.owner.id) == []
    rig.clock.t += 31  # the claimer crashed
    assert rig.tasks.flush_pending() == 1


def test_notification_to_gone_owner_is_dropped_result_kept(rig):
    t = rig.new()
    w = rig.as_pane(rig.pane(t))
    rig.backend.close(rig.owner)
    d = w.done(t["id"], "ok", summary="x")
    assert d["notify"] == "owner-gone" and d["status"] == "done"
    assert rig.reg.read()["pending"] == []


def test_pending_expires(rig):
    t = rig.new()
    rig.backend.state[rig.owner.id].input = "owner typing"
    rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="x")
    rig.clock.t += PENDING_TTL + 1
    rig.tasks.flush_pending()
    assert rig.reg.read()["pending"] == []
    assert rig.tasks.get(t["id"])["notify"] == "expired"


def test_long_summary_uses_the_file_rule(rig):
    t = rig.new()
    rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="z" * 900)
    [msg] = rig.typed(rig.owner.id)
    assert "read the file" in msg and "z" * 900 not in msg


# --- ls and wait --------------------------------------------------------


def test_ls_shows_own_open_tasks_unless_all(rig):
    mine = rig.new()
    theirs = rig.new(tasks=rig.as_pane(rig.other))
    ids = [t["id"] for t in rig.tasks.ls()]
    assert ids == [mine["id"]]
    assert {t["id"] for t in rig.tasks.ls(all=True)} == {mine["id"], theirs["id"]}


def test_ls_hides_closed_unless_all(rig):
    t = rig.new()
    rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="x")
    rig.tasks.close(t["id"])
    assert rig.tasks.ls() == []
    assert rig.tasks.ls(all=True)[0]["closed"] is True


def test_wait_returns_when_finished(rig):
    t = rig.new()
    worker = rig.as_pane(rig.pane(t))
    orig = rig.clock.sleep
    calls = {"n": 0}

    def sleep(s):
        calls["n"] += 1
        if calls["n"] == 3:
            worker.done(t["id"], "ok", summary="x")
        orig(s)

    rig.tasks.sleep = sleep
    assert rig.tasks.wait(t["id"], timeout=60)["status"] == "done"


def test_wait_times_out(rig):
    t = rig.new()
    with pytest.raises(WaitTimeout) as e:
        rig.tasks.wait(t["id"], timeout=10)
    assert e.value.code == 5


def test_wait_any(rig):
    a, b = rig.new(), rig.new()
    rig.as_pane(rig.pane(b)).done(b["id"], "failed", summary="no")
    assert rig.tasks.wait(any_task=True, timeout=10)["id"] == b["id"]


def test_wait_any_returns_each_result_once(rig):
    a, b = rig.new(), rig.new()
    rig.as_pane(rig.pane(a)).done(a["id"], "ok", summary="1")
    rig.as_pane(rig.pane(b)).done(b["id"], "ok", summary="2")
    assert rig.tasks.wait(any_task=True, timeout=1)["id"] == a["id"]
    assert rig.tasks.wait(any_task=True, timeout=1)["id"] == b["id"]
    with pytest.raises(UsageError):
        rig.tasks.wait(any_task=True, timeout=1)


def test_wait_any_without_running_tasks(rig):
    with pytest.raises(UsageError):
        rig.tasks.wait(any_task=True, timeout=1)


def test_wait_returns_on_lost(rig):
    t = rig.new()
    rig.backend.close(rig.pane(t))
    assert rig.tasks.wait(t["id"], timeout=10)["status"] == "lost"


# --- task close ---------------------------------------------------------


def test_task_close_refuses_running(rig):
    t = rig.new()
    with pytest.raises(GuardError) as e:
        rig.tasks.close(t["id"])
    assert e.value.code == 6
    assert ("close", t["pane"]) not in rig.backend.events


def test_task_close_force_marks_failed(rig):
    t = rig.new()
    rig.tasks.close(t["id"], force=True)
    got = rig.tasks.get(t["id"])
    assert ("close", t["pane"]) in rig.backend.events
    assert got["status"] == "failed" and got["closed"] and "closed" in got["summary"]


def test_task_close_after_done(rig):
    t = rig.new()
    rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="x")
    rig.backend.natives[t["pane"]] = "working: finishing its turn"
    rig.tasks.close(t["id"])
    assert ("close", t["pane"]) in rig.backend.events
    assert t["pane"] not in rig.reg.read()["panes"]


def test_task_close_with_pane_already_gone(rig):
    t = rig.new()
    rig.as_pane(rig.pane(t)).done(t["id"], "ok", summary="x")
    rig.backend.close(rig.pane(t))
    rig.tasks.close(t["id"])
    assert rig.tasks.get(t["id"])["closed"] is True


def test_task_close_never_closes_self(rig):
    t = rig.new()
    w = rig.pane(t)
    rig.as_pane(w).done(t["id"], "ok", summary="x")
    with pytest.raises(TargetError):
        rig.as_pane(w).close(t["id"], force=True)
    assert ("close", t["pane"]) not in rig.backend.events


# --- --reuse -----------------------------------------------------------


def test_reuse_clears_then_submits_brief(rig):
    t = rig.new(reuse="other")
    assert t["pane"] == rig.other.id
    typed = rig.typed(rig.other.id)
    assert typed[0] == "/clear" and t["id"] in typed[1] and "do the thing" in typed[1]
    assert not any(e[0] == "spawn" for e in rig.backend.events)


def test_reuse_refuses_busy_pane(rig):
    rig.backend.natives[rig.other.id] = "working: something"
    with pytest.raises(GuardError):
        rig.new(reuse="other", timeout=5)
    assert rig.typed(rig.other.id) == []
    assert rig.tasks.ls() == []


def test_reuse_refuses_draft(rig):
    rig.backend.state[rig.other.id].input = "someone typing"
    with pytest.raises(DraftError):
        rig.new(reuse="other")
    assert rig.typed(rig.other.id) == []


def test_reuse_refuses_harness_mismatch(rig):
    with pytest.raises(UsageError):
        rig.tasks.new("claude", title="x", brief="y", cwd="/w", reuse="other")


def test_reuse_refuses_pane_with_running_task(rig):
    t = rig.new()
    with pytest.raises(GuardError):
        rig.new(reuse=t["id"])


def test_reuse_self_refused(rig):
    with pytest.raises(TargetError):
        rig.new(reuse="owner")


def test_closing_a_reused_task_leaves_the_pane(rig):
    t = rig.new(reuse="other")
    rig.as_pane(rig.other).done(t["id"], "ok", summary="x")
    rig.tasks.close(t["id"], force=True)
    assert ("close", rig.other.id) not in rig.backend.events
    assert rig.tasks.get(t["id"])["closed"] is True


# --- close safety -------------------------------------------------------


def spawned(rig):
    return rig.tasks.spawn("generic", where="tab", cwd="/w", command="c", label="made")


def test_spawn_records_the_pane(rig):
    p = spawned(rig)
    assert p.id in rig.reg.read()["panes"]


def test_close_owned_idle_pane(rig):
    p = spawned(rig)
    rig.tasks.close_pane("made")
    assert ("close", p.id) in rig.backend.events
    assert p.id not in rig.reg.read()["panes"]


def test_close_refuses_unowned_pane(rig):
    with pytest.raises(GuardError) as e:
        rig.tasks.close_pane("other")
    assert e.value.code == 6
    assert ("close", rig.other.id) not in rig.backend.events


def test_close_force_on_unowned_pane(rig):
    rig.tasks.close_pane("other", force=True)
    assert ("close", rig.other.id) in rig.backend.events


def test_close_refuses_busy_pane(rig):
    p = spawned(rig)
    rig.backend.natives[p.id] = "working: long job"
    with pytest.raises(GuardError) as e:
        rig.tasks.close_pane("made")
    assert "busy" in str(e.value)
    rig.tasks.close_pane("made", force=True)
    assert ("close", p.id) in rig.backend.events


def test_close_refuses_self_even_with_force(rig):
    with pytest.raises(TargetError) as e:
        rig.tasks.close_pane("owner", force=True)
    assert e.value.code == 2
    assert ("close", rig.owner.id) not in rig.backend.events


def test_close_refuses_pane_of_running_task(rig):
    t = rig.new()
    with pytest.raises(GuardError) as e:
        rig.tasks.close_pane(t["id"])
    assert "task" in str(e.value)


def test_close_stale_record_is_not_owned(rig):
    p = spawned(rig)
    with rig.reg.update() as d:
        d["panes"][p.id]["ref"] = {"pid": "111"}
    rig.backend.server_ref = lambda: {"pid": "222"}
    with pytest.raises(GuardError):
        rig.tasks.close_pane("made")


# --- interrupt ----------------------------------------------------------


def test_interrupt_uses_the_profile_key():
    claude = make_pane(1, title="c", process="claude")
    me = make_pane(9, label="me")
    b = FakeBackend([claude, me], me=me)
    clock = FakeClock()
    ops = Ops(b, sender=me, clock=clock.now, sleep=clock.sleep)
    keys, verified = ops.interrupt(claude)
    assert keys == ("escape",) and verified
    assert b.inputs_sent(claude.id)[0][:3] == ("key", claude.id, "escape")


def test_interrupt_key_override():
    claude = make_pane(1, title="c", process="claude")
    me = make_pane(9, label="me")
    b = FakeBackend([claude, me], me=me)
    ops = Ops(b, sender=me, clock=FakeClock().now, sleep=lambda s: None)
    keys, verified = ops.interrupt(claude, key="ctrl-c")
    assert keys == ("ctrl-c",) and verified
    assert b.inputs_sent(claude.id)[0][2] == "ctrl-c"


def test_interrupt_unverified_profile_says_so():
    p = make_pane(1, title="c", process="opencode")
    me = make_pane(9, label="me")
    b = FakeBackend([p, me], me=me)
    ops = Ops(b, sender=me, clock=FakeClock().now, sleep=lambda s: None)
    assert ops.interrupt(p)[1] is False

"""Tasks: one harness session doing one job, tracked in the registry.

Also owns pane records, because `close` may only act on panes that were created here.
"""
from __future__ import annotations

import os

import secrets
import time
from pathlib import Path

from ..backends import make
from ..errors import BackendError, DraftError, GuardError, TargetError, TermctlError, UsageError, WaitTimeout
from ..pane import Pane
from ..resolve import resolve
from .ops import DEFAULT_TIMEOUT, Ops, harness_name, warn
from .profiles import PROFILES, for_process
from .registry import Registry

MAX_TASKS = 6
MAX_DEPTH = 2
TASK_INLINE = 500  # a brief up to this long is typed directly; longer goes through its file
WAIT_TIMEOUT = 600.0
WAIT_POLL = 3.0
NOTIFY_RETRY = 10.0  # how long `task done` keeps trying before it leaves the result pending
NOTIFY_POLL = 2.0
PENDING_TTL = 3600.0
CLAIM_TTL = 30.0  # a claim older than this belongs to a process that died
RESERVATION_TTL = 600.0  # a task with no pane after this long never got one
REUSE_IDLE_TIMEOUT = 5.0

ACTIVE = ("running", "blocked")
STATUS_OF = {"ok": "done", "failed": "failed", "blocked": "blocked"}


def _footer(task_id: str) -> str:
    return f'When finished, run: harness-ops task done {task_id} --status ok|failed|blocked --summary "<one line result>"'


def _env_int(name: str, default: int) -> int:
    raw = os.environ.get(name)
    if not raw:
        return default
    try:
        return int(raw)
    except ValueError:
        raise UsageError(f"{name} must be a whole number, got {raw!r}") from None


def _flat(text: str) -> str:
    return text.replace("\r", " ").replace("\x1b", "")


class Tasks:
    def __init__(self, registry: Registry, backend, sender: Pane | None = None, sender_ref: dict | None = None,
                 factory=None, clock=None, sleep=None, inbox: Path | None = None):
        self.reg = registry
        self.backend = backend
        self.sender = sender
        self.sender_ref = sender_ref or {}
        self.factory = factory or self._make
        self.clock = clock or time.time
        self.sleep = sleep or time.sleep
        self.inbox = inbox
        self._backends: dict[tuple, object] = {}

    # --- helpers --------------------------------------------------------

    def _make(self, pane_id: str, ref: dict):
        key = (pane_id.split(":", 1)[0], ref.get("socket_path"))
        if key not in self._backends:
            self._backends[key] = make(key[0], socket_path=key[1])
        return self._backends[key]

    def _ops(self, backend, sender=None) -> Ops:
        return Ops(backend, sender=sender, clock=self.clock, sleep=self.sleep, inbox=self.inbox)

    def _find(self, tasks: dict, task_id: str) -> dict:
        if task_id in tasks:
            return tasks[task_id]
        hits = [t for k, t in tasks.items() if len(task_id) >= 3 and k.startswith(task_id)]
        if len(hits) == 1:
            return hits[0]
        raise TargetError(f"{'no task' if not hits else 'several tasks'} match '{task_id}'")

    def _pane_by_id(self, pane_id: str, ref: dict) -> tuple[object, Pane | None]:
        backend = self.factory(pane_id, ref)
        pane = next((p for p in backend.list_panes() if p.id == pane_id), None)
        stored, current = ref.get("pid"), backend.server_ref().get("pid") if pane else None
        if pane and stored and current != stored:
            pane = None  # the terminal server restarted: the same ID is now a different pane
        return backend, pane

    def _owner(self) -> Pane:
        if self.sender is None:
            raise UsageError("cannot tell which pane this runs in; a task needs an owner pane")
        return self.sender

    # --- reconcile ------------------------------------------------------

    def reconcile(self) -> None:
        """Mark active tasks whose pane is gone as lost. Unknown is not gone: a backend error changes nothing."""
        tasks = self.reg.read()["tasks"]
        lost: list[str] = []
        now = self.clock()
        for t in tasks.values():
            if t["status"] not in ACTIVE:
                continue
            if not t.get("pane"):
                if now - t["started"] > RESERVATION_TTL:
                    lost.append(t["id"])
                continue
            try:
                _, pane = self._pane_by_id(t["pane"], t.get("ref") or {})
            except TermctlError as e:
                warn(f"cannot check task {t['id']}: {e}")
                continue
            if pane is None:
                lost.append(t["id"])
        if not lost:
            return
        with self.reg.update() as d:
            for tid in lost:
                t = d["tasks"].get(tid)
                if t and t["status"] in ACTIVE:
                    t.update(status="lost", ended=self.clock(), summary=t.get("summary") or "its pane is gone")
                    d["panes"].pop(t.get("pane"), None)

    # --- new ------------------------------------------------------------

    def _reserve(self, owner: Pane, harness: str, title: str, cwd: str, max_tasks: int | None, pane: Pane | None) -> dict:
        limit = max_tasks if max_tasks is not None else _env_int("TERMCTL_MAX_TASKS", MAX_TASKS)
        depth_limit = _env_int("TERMCTL_MAX_DEPTH", MAX_DEPTH)
        with self.reg.update() as d:
            tasks = d["tasks"]
            running = [t for t in tasks.values() if t["status"] == "running" and t["owner"] == owner.id]
            if len(running) >= limit:
                raise GuardError(f"{owner.address} already runs {len(running)} tasks (limit {limit}); use --max to raise it")
            parent = next((t for t in tasks.values() if t["status"] in ACTIVE and t.get("pane") == owner.id), None)
            depth = parent["depth"] + 1 if parent else 1
            if depth > depth_limit:
                raise GuardError(f"task depth {depth} is over the limit {depth_limit}; set TERMCTL_MAX_DEPTH to change it")
            if pane is not None and any(t["status"] in ACTIVE and t.get("pane") == pane.id for t in tasks.values()):
                raise GuardError(f"{pane.address} already holds a running task")
            tid = "t" + secrets.token_hex(3)
            task = {
                "id": tid, "title": title, "harness": harness, "pane": pane.id if pane else None,
                "ref": self.backend.server_ref(), "owner": owner.id, "owner_ref": self.sender_ref,
                "cwd": cwd, "brief": None, "status": "running", "started": self.clock(), "ended": None,
                "summary": "", "depth": depth, "notify": "none", "closed": False, "owned": pane is None,
            }
            tasks[tid] = task
            return dict(task)

    def _write_brief(self, tid: str, text: str) -> Path:
        d = self.reg.root / "briefs"
        d.mkdir(parents=True, exist_ok=True)
        path = d / f"{tid}.md"
        path.write_text(f"{text.rstrip()}\n\n{_footer(tid)}\n")
        return path

    def _prompt(self, tid: str, owner: Pane, text: str, brief: Path) -> str:
        body = _flat(text.strip())
        name = owner.label or owner.title or harness_name(owner)
        if "\n" in body or len(body) > TASK_INLINE:
            body = f"Read the file {brief} and do what it says."
        return f"[task {tid} from {name}] {body} {_footer(tid)}"

    def _fail(self, tid: str, why: str) -> None:
        with self.reg.update() as d:
            d["tasks"][tid].update(status="failed", ended=self.clock(), summary=why)

    def new(self, harness: str, title: str, brief: str | None = None, brief_file: str | None = None, cwd: str | None = None,
            where: str = "tab", reuse: str | None = None, command: str | None = None, max_tasks: int | None = None,
            timeout: float = DEFAULT_TIMEOUT) -> dict:
        if (brief is None) == (brief_file is None):
            raise UsageError("use exactly one of --brief or --brief-file")
        if brief_file is not None:
            path = Path(brief_file).expanduser().resolve()
            if not path.is_file():
                raise UsageError(f"--brief-file: no such file: {path}")
            brief = path.read_text()
        if not brief.strip():
            raise UsageError("empty brief")
        owner = self._owner()
        ops = self._ops(self.backend, owner)
        pane = None
        if reuse is not None:
            pane = ops.pick(reuse)
            prof = for_process(pane.process)
            if prof.name != harness:
                raise UsageError(f"{pane.address} runs {prof.name}, not {harness}")
            if any(t["status"] in ACTIVE and t.get("pane") == pane.id for t in self.reg.read()["tasks"].values()):
                raise GuardError(f"{pane.address} already holds a running task")
            try:
                ops.wait_idle(pane, min(timeout, REUSE_IDLE_TIMEOUT), prof)
            except WaitTimeout as e:
                raise GuardError(f"{pane.address} is not idle; a task can only reuse an idle pane") from e
            ops._check_draft(pane, prof, force=False)
        task = self._reserve(owner, harness, title, cwd or os.getcwd(), max_tasks, pane)
        tid = task["id"]
        try:
            brief_path = self._write_brief(tid, brief)
            with self.reg.update() as d:
                d["tasks"][tid]["brief"] = str(brief_path)
            if pane is None:
                pane = self._spawn(ops, harness, where, task["cwd"], tid, title, command, timeout, tid)
            else:
                prof = for_process(pane.process)
                ops.clear(pane, timeout=timeout)
                ops.assert_cleared(pane, prof)
            ops.submit(pane, self._prompt(tid, owner, brief, brief_path))
        except TermctlError as e:
            self._fail(tid, f"could not start: {e}".splitlines()[0])
            raise
        return self.reg.read()["tasks"][tid]

    def _spawn(self, ops: Ops, harness, where, cwd, label, title, command, timeout, tid) -> Pane:
        ref = self.backend.server_ref()

        def created(pane: Pane) -> None:
            with self.reg.update() as d:
                self._record(d, pane, harness, label, cwd, ref)
                d["tasks"][tid]["pane"] = pane.id

        return ops.spawn(harness, where, cwd, label=label, title=title, command=command, timeout=timeout, on_created=created)

    def _record(self, d: dict, pane: Pane, harness: str, label, cwd, ref: dict) -> None:
        d["panes"][pane.id] = {"harness": harness, "label": label, "cwd": cwd, "ref": ref,
                               "owner": self.sender.id if self.sender else None, "created": self.clock()}

    # --- spawn and close -------------------------------------------------

    def spawn(self, harness: str, where: str, cwd: str | None, prompt: str | None = None, prompt_file: str | None = None,
              label: str | None = None, command: str | None = None, timeout: float = DEFAULT_TIMEOUT) -> Pane:
        ref = self.backend.server_ref()

        def created(pane: Pane) -> None:
            with self.reg.update() as d:
                self._record(d, pane, harness, label, cwd, ref)

        return self._ops(self.backend, self.sender).spawn(
            harness, where, cwd, prompt=prompt, prompt_file=prompt_file, label=label, command=command,
            timeout=timeout, on_created=created)

    def _is_self(self, pane_id: str) -> bool:
        return self.sender is not None and pane_id == self.sender.id

    def _busy(self, backend, pane: Pane) -> bool:
        native = backend.native_status(pane)
        return bool(native) and native.startswith("working") or for_process(pane.process).busy(backend.capture(pane, 200))

    def close_pane(self, target: str, force: bool = False) -> Pane:
        panes = self.backend.list_panes()
        pane = resolve(panes, target)
        if pane.is_self or self._is_self(pane.id):
            raise TargetError("refusing to close your own pane")
        data = self.reg.read()
        record = data["panes"].get(pane.id)
        owned = record is not None
        if owned and record.get("ref", {}).get("pid") and self.backend.server_ref().get("pid") != record["ref"]["pid"]:
            owned = False  # the record is from before a terminal server restart
        task = next((t for t in data["tasks"].values() if t["status"] in ACTIVE and t.get("pane") == pane.id), None)
        if not force:
            if not owned:
                raise GuardError(f"{pane.address} was not created through spawn or task new; use --force to close it")
            if task:
                raise GuardError(f"{pane.address} holds running task {task['id']}; use task close, or --force")
            if self._busy(self.backend, pane):
                raise GuardError(f"{pane.address} is busy; use --force to close it")
        self.backend.close(pane)
        with self.reg.update() as d:
            d["panes"].pop(pane.id, None)
            for t in d["tasks"].values():
                if t.get("pane") == pane.id and t["status"] in ACTIVE:
                    t.update(status="failed", ended=self.clock(), summary="its pane was closed with --force", closed=True)
        return pane

    # --- read ------------------------------------------------------------

    def ls(self, all: bool = False) -> list[dict]:
        self.reconcile()
        tasks = sorted(self.reg.read()["tasks"].values(), key=lambda t: t["started"])
        if all or self.sender is None:
            return tasks if all else [t for t in tasks if not t["closed"]]
        return [t for t in tasks if t["owner"] == self.sender.id and not t["closed"]]

    def get(self, task_id: str) -> dict:
        self.reconcile()
        return self._find(self.reg.read()["tasks"], task_id)

    # --- send -------------------------------------------------------------

    def send(self, task_id: str, text: str, force: bool = False) -> None:
        t = self.get(task_id)
        if t["status"] == "lost" or t["closed"]:
            raise UsageError(f"task {t['id']} is {'closed' if t['closed'] else 'lost'}: its pane is gone")
        backend, pane = self._pane_by_id(t["pane"], t.get("ref") or {})
        if pane is None:
            raise UsageError(f"task {t['id']}: its pane is gone")
        self._ops(backend, self.sender).msg(pane, f"task {t['id']}: {text} ({_footer(t['id'])})", force=force)
        if t["status"] != "running":  # new work on a finished task: it must report again
            with self.reg.update() as d:
                d["tasks"][t["id"]].update(status="running", ended=None, notify="none", seen=False)

    # --- wait -------------------------------------------------------------

    def _unseen(self, tasks: dict, owner: str) -> list[dict]:
        """Results this owner has not collected with `wait` yet, oldest first."""
        done = [t for t in tasks.values() if t["owner"] == owner and t["status"] != "running"
                and not t["closed"] and not t.get("seen")]
        return sorted(done, key=lambda t: t.get("ended") or 0)

    def wait(self, task_id: str | None = None, any_task: bool = False, timeout: float = WAIT_TIMEOUT) -> dict:
        """--any returns each finished task once, so repeated calls walk through the results."""
        tid = None if any_task else self.get(task_id)["id"]
        me = self._owner().id if any_task else None
        deadline = self.clock() + timeout
        while True:
            self.reconcile()
            self.flush_pending()
            tasks = self.reg.read()["tasks"]
            if any_task:
                found = self._unseen(tasks, me)
                if not found and not any(t["status"] == "running" and t["owner"] == me for t in tasks.values()):
                    raise UsageError("no running tasks and no uncollected results to wait for")
            else:
                found = [tasks[tid]] if tasks[tid]["status"] != "running" else []
            if found:
                with self.reg.update() as d:
                    d["tasks"][found[0]["id"]]["seen"] = True
                    return d["tasks"][found[0]["id"]]
            if self.clock() >= deadline:
                what = "no task of yours" if any_task else f"task {tid}"
                raise WaitTimeout(f"{what} finished within {timeout:g}s")
            self.sleep(WAIT_POLL)

    # --- done -------------------------------------------------------------

    def done(self, task_id: str, status: str, summary: str | None = None, summary_file: str | None = None) -> dict:
        if status not in STATUS_OF:
            raise UsageError(f"--status must be one of {', '.join(STATUS_OF)}")
        if summary_file is not None:
            summary = Path(summary_file).expanduser().read_text()
        if not summary or not summary.strip():
            raise UsageError("task done needs --summary or --summary-file")
        summary = summary.strip()
        with self.reg.update() as d:
            t = self._find(d["tasks"], task_id)
            if self.sender is not None and self.sender.id not in (t.get("pane"), t["owner"]):
                raise TargetError(f"{self.sender.address} is neither the worker nor the owner of task {t['id']}")
            if t["status"] not in ACTIVE:
                raise UsageError(f"task {t['id']} is already {t['status']}")
            t.update(status=STATUS_OF[status], ended=self.clock(), summary=summary, notify="pending")
            tid = t["id"]
            d["pending"].append({
                "task": tid, "owner": t["owner"], "owner_ref": t.get("owner_ref") or {},
                "tag": self._done_tag(t), "text": f"RE: task {tid} ({t.get('title', '')}) {t['status']}: {summary}",
                "created": self.clock(), "claimed": None,
            })
        deadline = self.clock() + NOTIFY_RETRY
        while True:
            self.flush_pending()
            t = self.reg.read()["tasks"][tid]
            if t["notify"] != "pending" or self.clock() >= deadline:
                return t
            self.sleep(NOTIFY_POLL)

    def _done_tag(self, task: dict) -> str:
        if self.sender is not None:
            return self._ops(self.backend, self.sender)._tag()
        return f"[agent-msg from {task['id']} ({task.get('harness', '-')}) id={task.get('pane')} folder={task.get('cwd')}]"

    # --- pending notifications --------------------------------------------

    def flush_pending(self) -> int:
        if not self.reg.read()["pending"]:
            return 0
        now = self.clock()
        with self.reg.update() as d:
            keep, claimed = [], []
            for p in d["pending"]:
                if now - p["created"] > PENDING_TTL:
                    d["tasks"][p["task"]]["notify"] = "expired"
                    continue
                if not p.get("claimed") or now - p["claimed"] > CLAIM_TTL:
                    p["claimed"] = now
                    claimed.append(dict(p))
                keep.append(p)
            d["pending"] = keep
        delivered = 0
        for p in claimed:
            outcome = self._deliver(p)
            delivered += outcome == "delivered"
            with self.reg.update() as d:
                for q in d["pending"]:
                    if q["task"] == p["task"] and q["created"] == p["created"]:
                        if outcome == "retry":
                            q["claimed"] = None
                        else:
                            d["tasks"][p["task"]]["notify"] = outcome
                            d["pending"].remove(q)
                        break
        return delivered

    def _deliver(self, p: dict) -> str:
        try:
            backend, pane = self._pane_by_id(p["owner"], p["owner_ref"])
            if pane is None:
                return "owner-gone"
            self._ops(backend).msg(pane, p["text"], tag=p["tag"])
            return "delivered"
        except DraftError:
            return "retry"
        except (BackendError, UsageError, TargetError) as e:
            warn(f"could not notify the owner of task {p['task']}: {e}")
            return "retry"

    # --- task close -------------------------------------------------------

    def close(self, task_id: str, force: bool = False) -> dict:
        t = self.get(task_id)
        if self._is_self(t.get("pane") or ""):
            raise TargetError("refusing to close your own pane")
        if t["status"] == "running" and not force:
            raise GuardError(f"task {t['id']} is running; use --force to close it")
        if t.get("owned") and t.get("pane"):
            backend, pane = self._pane_by_id(t["pane"], t.get("ref") or {})
            if pane is not None:
                backend.close(pane)
        with self.reg.update() as d:
            cur = d["tasks"][t["id"]]
            cur["closed"] = True
            if cur["status"] == "running":
                cur.update(status="failed", ended=self.clock(), summary="closed by its owner before it finished")
            if cur.get("owned"):
                d["panes"].pop(cur.get("pane"), None)
            return cur

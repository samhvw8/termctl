"""Harness operations: draft check, slash commands, idle waits, spawn, tagged messages."""
from __future__ import annotations

import re
import sys
import time
from pathlib import Path

from ..backends.base import Backend
from ..errors import DraftError, TargetError, UsageError, WaitTimeout
from ..pane import Pane
from ..resolve import resolve
from .profiles import PROFILES, Profile, for_process

MAX_INLINE = 800  # longer messages, or multi-line ones, go through a file
ENTER_DELAY = 0.4  # TUIs drop an Enter that arrives with the pasted text
SETTLE = 0.5  # let the screen react to what we sent before judging it
POLL = 0.25
DEFAULT_TIMEOUT = 120.0
CAPTURE_LINES = 200
INBOX = Path.home() / ".cache" / "agent-msg"


def harness_name(pane: Pane) -> str:
    prof = for_process(pane.process)
    return prof.name if prof.name != "generic" else (pane.process or "-")


def warn(msg: str) -> None:
    print(f"harness-ops: warning: {msg}", file=sys.stderr)


class Ops:
    def __init__(self, backend: Backend, sender: Pane | None = None, clock=None, sleep=None, inbox: Path | None = None):
        self.backend = backend
        self.sender = sender
        self.now = clock or time.monotonic
        self.sleep = sleep or time.sleep
        self.inbox = inbox or INBOX

    # --- targets -------------------------------------------------------

    def pick(self, target: str) -> Pane:
        """Resolve a target this caller may act on. Your own pane is refused."""
        pane = resolve(self.backend.list_panes(), target)
        if self.sender is not None and pane.id == self.sender.id:
            raise TargetError("refusing to send to your own pane")
        return pane

    def _capture(self, pane: Pane) -> list[str]:
        return self.backend.capture(pane, CAPTURE_LINES)

    # --- draft check ---------------------------------------------------

    def _draft(self, pane: Pane, prof: Profile) -> str | None:
        return prof.read_input(self._capture(pane))

    def _check_draft(self, pane: Pane, prof: Profile, force: bool) -> None:
        draft = self._draft(pane, prof)
        if draft is None:
            if prof.calibrated and not force:
                raise DraftError(f"cannot see {prof.name}'s input line on {pane.address}; use --force to send anyway")
            warn(f"cannot read the input line of {pane.address} ({prof.name}); a draft there would be joined")
        elif draft and not force:
            raise DraftError(f"{pane.address} has a draft on its input line: {draft[:80]!r}; use --force to send anyway")

    def _assert_alone(self, pane: Pane, prof: Profile, force: bool) -> None:
        """A slash command must be the whole line. --force wipes a draft rather than joining it."""
        self._check_draft(pane, prof, force)
        if not force:
            return
        draft = self._draft(pane, prof)
        if draft:
            self.backend.send_key(pane, "ctrl-u")
            self.sleep(ENTER_DELAY)
            if self._draft(pane, prof):
                raise DraftError(f"--force could not empty the input line of {pane.address}")

    # --- typing --------------------------------------------------------

    def _submit(self, pane: Pane, line: str) -> None:
        self.backend.send_text(pane, line)
        self.sleep(ENTER_DELAY)
        self.backend.send_key(pane, "enter")

    # --- idle ----------------------------------------------------------

    def wait_idle(self, pane: Pane, timeout: float = DEFAULT_TIMEOUT, profile: Profile | None = None) -> None:
        prof = profile or for_process(pane.process)
        deadline = self.now() + timeout
        self.sleep(SETTLE)
        last, since = None, self.now()
        while True:
            lines = self._capture(pane)
            view = prof.stable_view(lines)
            if view != last:
                last, since = view, self.now()
            native = self.backend.native_status(pane)
            native_busy = bool(native) and native.startswith("working")
            prompt_ok = not prof.prompt_required or prof.read_input(lines) is not None
            if not native_busy and prompt_ok and not prof.busy(lines) and self.now() - since >= prof.stable_seconds:
                return
            if self.now() >= deadline:
                tail = "\n".join(f"  | {ln[:100]}" for ln in lines[-6:])
                raise WaitTimeout(f"{pane.address} still not idle after {timeout:g}s (native: {native}). Screen:\n{tail}")
            self.sleep(POLL)

    # --- slash and clear ----------------------------------------------

    def slash(self, pane: Pane, command: str, force: bool = False, wait: bool = False, timeout: float = DEFAULT_TIMEOUT) -> None:
        command = command.strip()
        if not re.fullmatch(r"/\S+( [^\n\r]*)?", command):
            raise UsageError(f"a slash command is one line starting with '/': got {command!r}")
        prof = for_process(pane.process)
        if not prof.slash:
            raise UsageError(f"{prof.name} has no slash commands")
        self._assert_alone(pane, prof, force)
        self._submit(pane, command)
        if wait:
            self.wait_idle(pane, timeout, prof)

    def clear(self, pane: Pane, force: bool = False, timeout: float = DEFAULT_TIMEOUT) -> None:
        self.slash(pane, "/clear", force=force, wait=True, timeout=timeout)

    # --- messages ------------------------------------------------------

    def _tag(self) -> str:
        me = self.sender
        if me is None:
            raise UsageError("cannot tell which pane this runs in; run inside a terminal pane (backend env vars not set)")
        harness = harness_name(me)
        name = me.label or me.title or harness
        return f"[agent-msg from {name} ({harness}) id={me.address} folder={me.folder}]"

    def _write_inbox(self, body: str, to: Pane) -> Path:
        self.inbox.mkdir(parents=True, exist_ok=True)
        me = re.sub(r"[^A-Za-z0-9._-]", "_", self.sender.address[:8]) if self.sender else "spawn"
        to_id = re.sub(r"[^A-Za-z0-9._-]", "_", to.address[:8])
        f = self.inbox / f"{time.strftime('%Y%m%d-%H%M%S')}-{me}-to-{to_id}.md"
        f.write_text(body)
        return f

    def msg(self, pane: Pane, text: str, force: bool = False, clear_first: bool = False, timeout: float = DEFAULT_TIMEOUT) -> str:
        text = text.strip()
        if not text:
            raise UsageError("empty message")
        tag = self._tag()
        prof = for_process(pane.process)
        if clear_first:
            self.clear(pane, force=force, timeout=timeout)
            # Even with --force: leftover text here means /clear never ran, and
            # typing now would rebuild the joined line this option exists to stop.
            left = self._draft(pane, prof)
            if left:
                raise DraftError(f"input line of {pane.address} still holds {left[:80]!r} after /clear; message not sent")
        else:
            self._check_draft(pane, prof, force)
        if "\n" in text or len(text) > MAX_INLINE:
            f = self._write_inbox(f"{tag}\n\n{text}\n", pane)
            line = f"{tag} Long message; read the file {f}"
        else:
            line = f"{tag} {text}"
        line = line.replace("\r", " ").replace("\x1b", "")
        self._submit(pane, line)
        return line

    # --- spawn ---------------------------------------------------------

    def spawn(self, harness: str, where: str, cwd: str | None, prompt: str | None = None, prompt_file: str | None = None,
              label: str | None = None, command: str | None = None, timeout: float = DEFAULT_TIMEOUT) -> Pane:
        if prompt is not None and prompt_file is not None:
            raise UsageError("use --prompt or --prompt-file, not both")
        if harness not in PROFILES and not command:
            raise UsageError(f"unknown harness '{harness}'. Known: {', '.join(PROFILES)}; or pass --command")
        prof = PROFILES.get(harness, PROFILES["generic"])
        cmd = command or prof.command
        if not cmd:
            raise UsageError(f"harness '{harness}' has no default command; pass --command")
        if prompt_file is not None:
            path = Path(prompt_file).expanduser().resolve()
            if not path.is_file():
                raise UsageError(f"--prompt-file: no such file: {path}")
            prompt = path.read_text()
        pane = self.backend.spawn(where, cmd, cwd=cwd, title=label)
        if label:
            self.backend.set_label(pane, label)
        self.wait_idle(pane, timeout, prof)
        if prompt is not None and prompt.strip():
            self._submit(pane, self._first_line(prompt, prompt_file, pane))
        return pane

    def _first_line(self, prompt: str, prompt_file: str | None, pane: Pane) -> str:
        text = prompt.strip()
        if "\n" not in text and len(text) <= MAX_INLINE:
            return text.replace("\r", " ").replace("\x1b", "")
        path = Path(prompt_file).expanduser().resolve() if prompt_file else self._write_inbox(text + "\n", pane)
        return f"Read the file {path} and follow its instructions."

    # --- status --------------------------------------------------------

    def status(self, pane: Pane, native: str | None) -> str:
        """One-shot status for `ls`: the terminal's own report, else a single screen sample."""
        if native:
            return native
        prof = for_process(pane.process)
        if prof.name == "generic":
            return "?"
        try:
            lines = self._capture(pane)
        except Exception:
            return "?"
        if prof.busy(lines):
            return "working"
        return "idle" if prof.read_input(lines) is not None and prof.prompt_required else "?"

"""iTerm2 backend, through iTerm's Python API. Keeps one websocket for the process lifetime."""
from __future__ import annotations

import asyncio
import json
import os
import shlex
import subprocess

from ..errors import BackendError, UsageError
from ..pane import Pane
from ..proc import clean_title, cwd_of, main_process, processes_by_tty, tilde
from .base import WHERE, Backend, check_text, normalize_key, trim_screen

NATIVE_IT2 = "/Applications/iTerm.app/Contents/Resources/utilities/it2"
LABEL_VAR = "user.agent"
KEYS = {
    "enter": "\r", "escape": "\x1b", "tab": "\t", "backspace": "\x7f",
    "up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
}


def key_bytes(key: str) -> str:
    k = normalize_key(key)
    if k in KEYS:
        return KEYS[k]
    return chr(ord(k[-1]) - ord("a") + 1)  # ctrl-<letter>


class ITerm2Backend(Backend):
    name = "iterm2"

    def __init__(self):
        self._loop = None
        self._conn = None

    def _run(self, coro_fn):
        import iterm2  # imported late: other backends must work without it being reachable

        if self._conn is None:
            try:
                self._loop = asyncio.new_event_loop()
                asyncio.set_event_loop(self._loop)
                self._conn = self._loop.run_until_complete(iterm2.Connection.async_create())
            except Exception as e:
                raise BackendError(
                    f"cannot reach the iTerm2 API: {type(e).__name__}: {e}. Is the Python API enabled "
                    "(Settings > General > Magic > Enable Python API)?"
                ) from e
        try:
            return self._loop.run_until_complete(coro_fn(iterm2, self._conn))
        except (BackendError, UsageError):
            raise
        except Exception as e:
            raise BackendError(f"iTerm2 API error: {type(e).__name__}: {e}") from e

    @staticmethod
    def _self_native() -> str:
        return os.environ.get("ITERM_SESSION_ID", "").split(":", 1)[-1]

    def available(self) -> bool:
        return os.path.exists("/Applications/iTerm.app") and bool(self._self_native() or os.environ.get("ITERM2_COOKIE"))

    def list_panes(self) -> list[Pane]:
        async def go(iterm2, conn):
            app = await iterm2.async_get_app(conn)
            rows = []
            for w in app.terminal_windows:
                for t in w.tabs:
                    for s in t.sessions:
                        rows.append((
                            s.session_id,
                            (await s.async_get_variable("tty") or "").replace("/dev/", ""),
                            clean_title(await s.async_get_variable("autoName") or ""),
                            await s.async_get_variable(LABEL_VAR) or "",
                        ))
            return rows

        rows = self._run(go)
        procs = processes_by_tty()
        picks = {sid: main_process(procs.get(tty, [])) for sid, tty, _, _ in rows}
        cwds = cwd_of([pid for pid, _ in picks.values() if pid])
        me = self._self_native()
        panes = []
        for sid, tty, title, label in rows:
            pid, name = picks[sid]
            panes.append(Pane(
                id=f"iterm2:{sid}", backend="iterm2", native=sid, tty=tty, title=title,
                label=label, process=name or "-", folder=tilde(cwds.get(pid, "")), is_self=sid == me,
            ))
        return panes

    def self_pane(self):
        me = self._self_native()
        if not me:
            return None
        return next((p for p in self.list_panes() if p.is_self), None)

    def _session(self, app, pane):
        s = app.get_session_by_id(pane.native)
        if s is None:
            raise BackendError(f"iTerm2 session {pane.native} no longer exists")
        return s

    def spawn(self, where, cmd, cwd=None, title=None) -> Pane:
        if where not in WHERE:
            raise UsageError(f"--where must be one of {', '.join(WHERE)}")

        async def go(iterm2, conn):
            app = await iterm2.async_get_app(conn)
            prof = iterm2.LocalWriteOnlyProfile()
            if cmd:
                # A login, interactive shell loads mise and PATH; the bare command would not.
                shell = os.environ.get("SHELL", "/bin/zsh")
                prof.set_use_custom_command("Yes")
                prof.set_command(shlex.join([shell, "-lic", cmd]))
            if cwd:
                prof.set_initial_directory_mode(iterm2.InitialWorkingDirectory.INITIAL_WORKING_DIRECTORY_CUSTOM)
                prof.set_custom_directory(cwd)
            anchor = app.get_session_by_id(self._self_native()) if self._self_native() else None
            if where == "window":
                win = await iterm2.Window.async_create(conn, profile_customizations=prof)
                session = win.current_tab.current_session
            elif anchor is None:
                raise UsageError(f"--where {where} needs a pane to anchor on: run inside an iTerm2 pane")
            elif where == "split":
                session = await anchor.async_split_pane(vertical=True, profile_customizations=prof)
            else:
                win, _ = app.get_tab_and_window_for_session(anchor)
                previous = win.current_tab
                tab = await win.async_create_tab(profile_customizations=prof)
                session = tab.current_session
                await previous.async_select()  # keep the user's focus where it was
            if title:
                await session.async_set_name(title)
            return session.session_id

        sid = self._run(go)
        return Pane(id=f"iterm2:{sid}", backend="iterm2", native=sid, title=title or "")

    def send_text(self, pane, text):
        check_text(text)

        async def go(iterm2, conn):
            app = await iterm2.async_get_app(conn)
            await self._session(app, pane).async_send_text(text)

        self._run(go)

    def send_key(self, pane, key):
        data = key_bytes(key)

        async def go(iterm2, conn):
            app = await iterm2.async_get_app(conn)
            await self._session(app, pane).async_send_text(data)

        self._run(go)

    def capture(self, pane, lines=200):
        async def go(iterm2, conn):
            app = await iterm2.async_get_app(conn)
            sc = await self._session(app, pane).async_get_screen_contents()
            return [sc.line(i).string for i in range(sc.number_of_lines)]

        return trim_screen(self._run(go), lines)

    def close(self, pane):
        async def go(iterm2, conn):
            app = await iterm2.async_get_app(conn)
            await self._session(app, pane).async_close(force=True)

        self._run(go)

    def set_label(self, pane, label):
        async def go(iterm2, conn):
            app = await iterm2.async_get_app(conn)
            await self._session(app, pane).async_set_variable(LABEL_VAR, label)

        self._run(go)

    def native_status(self, pane):
        """Busy/idle from iTerm2's own it2 (Claude Code sets it). None when unavailable."""
        if not os.path.exists(NATIVE_IT2):
            return None
        try:
            # The iterm2 library leaves its own cookie in os.environ; the native CLI
            # must authenticate by itself, so drop it from the child's environment.
            env = {k: v for k, v in os.environ.items() if k not in ("ITERM2_COOKIE", "ITERM2_KEY")}
            r = subprocess.run([NATIVE_IT2, "session", "get-status", "-s", pane.native],
                               capture_output=True, text=True, timeout=5, env=env)
            d = json.loads(r.stdout)
            st = d.get("status")
            if not st:
                return None
            return f"{st}: {d['detail']}" if d.get("detail") else st
        except Exception:
            return None

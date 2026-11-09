"""tmux backend, through the tmux CLI."""
from __future__ import annotations

import os
import re
import shutil
import subprocess

from ..errors import BackendError, UsageError
from ..pane import Pane
from ..proc import clean_title, main_process, processes_by_tty, tilde
from .base import WHERE, Backend, check_text, normalize_key, trim_screen

SEP = "|@@|"
LABEL_OPT = "@termctl_label"
FIELDS = ["pane_id", "pane_tty", "pane_title", LABEL_OPT, "pane_current_path", "pane_current_command"]
KEYS = {
    "enter": "Enter", "escape": "Escape", "tab": "Tab", "backspace": "BSpace",
    "up": "Up", "down": "Down", "left": "Left", "right": "Right",
}


class TmuxBackend(Backend):
    name = "tmux"

    def __init__(self, socket: str | None = None, socket_path: str | None = None):
        self.socket = socket
        self.socket_path = socket_path

    def _argv(self, *args) -> list[str]:
        if self.socket_path:
            return ["tmux", "-S", self.socket_path, *args]
        return ["tmux", *(["-L", self.socket] if self.socket else []), *args]

    def _run(self, *args) -> str:
        try:
            r = subprocess.run(self._argv(*args), capture_output=True, text=True)
        except FileNotFoundError:
            raise BackendError("tmux is not installed") from None
        if r.returncode != 0:
            raise BackendError(f"tmux {args[0]}: {r.stderr.strip() or 'failed'}")
        return r.stdout

    def available(self) -> bool:
        if not shutil.which("tmux"):
            return False
        return subprocess.run(self._argv("list-sessions"), capture_output=True).returncode == 0

    def server_ref(self) -> dict:
        try:
            path, pid = self._run("display-message", "-p", "#{socket_path}" + SEP + "#{pid}").strip().split(SEP)
        except (BackendError, ValueError):
            return {}
        return {"socket_path": path, "pid": pid}

    def _self_native(self) -> str:
        pane = os.environ.get("TMUX_PANE", "")
        tmux_env = os.environ.get("TMUX", "")
        if not pane or not tmux_env:
            return ""
        # TMUX_PANE names a pane on the server in $TMUX only. On another socket
        # the same %N is a different pane, so identity must not carry over.
        try:
            here = self._run("display-message", "-p", "#{socket_path}").strip()
        except BackendError:
            return ""
        return pane if here == tmux_env.split(",")[0] else ""

    def list_panes(self) -> list[Pane]:
        fmt = SEP.join("#{" + f + "}" for f in FIELDS)
        try:
            out = self._run("list-panes", "-a", "-F", fmt)
        except BackendError as e:
            if "no server running" in str(e) or "error connecting" in str(e):
                return []  # a server that is not running has no panes
            raise
        procs = processes_by_tty()
        me = self._self_native()
        panes: list[Pane] = []
        for line in out.splitlines():
            fields = line.split(SEP)
            if len(fields) != len(FIELDS):
                raise BackendError(f"tmux list-panes: cannot parse line: {line!r}")
            native, tty, title, label, path, cur = fields
            tty = tty.replace("/dev/", "")
            _, proc = main_process(procs.get(tty, []))
            panes.append(Pane(
                id=f"tmux:{native}", backend="tmux", native=native, tty=tty,
                title=clean_title(title), label=label, process=proc or cur,
                folder=tilde(path), is_self=native == me,
            ))
        return panes

    def self_pane(self) -> Pane | None:
        return next((p for p in self.list_panes() if p.is_self), None)

    def spawn(self, where, cmd, cwd=None, title=None) -> Pane:
        if where not in WHERE:
            raise UsageError(f"--where must be one of {', '.join(WHERE)}")
        args = ["-P", "-F", "#{pane_id}"] + (["-c", cwd] if cwd else [])
        if where == "tab":
            argv = ["new-window", "-d", *args]
            if title:
                argv += ["-n", title]
        elif where == "split":
            me = self.self_pane()
            if me is None:
                raise UsageError("split needs a pane to split: run inside tmux on this server")
            argv = ["split-window", "-d", "-h", "-t", me.native, *args]
        else:
            name = re.sub(r"[^A-Za-z0-9_-]", "-", title or "termctl") + f"-{os.getpid()}"
            argv = ["new-session", "-d", "-s", name, *args]
        if cmd:
            argv += ["--", cmd]
        native = self._run(*argv).strip()
        if title:
            self._run("select-pane", "-t", native, "-T", title)
        return next((p for p in self.list_panes() if p.native == native),
                    Pane(id=f"tmux:{native}", backend="tmux", native=native, title=title or ""))

    def send_text(self, pane, text):
        check_text(text)
        # tmux reads a trailing ';' of any argument as a command separator.
        if text.endswith(";"):
            text = text[:-1] + "\\;"
        self._run("send-keys", "-l", "-t", pane.native, "--", text)

    def send_key(self, pane, key):
        k = normalize_key(key)
        self._run("send-keys", "-t", pane.native, KEYS.get(k) or "C-" + k.split("-")[1])

    def capture(self, pane, lines=200):
        out = self._run("capture-pane", "-p", "-t", pane.native, "-S", f"-{lines}")
        return trim_screen(out.splitlines(), lines)

    def close(self, pane):
        self._run("kill-pane", "-t", pane.native)

    def set_label(self, pane, label):
        self._run("set-option", "-p", "-t", pane.native, LABEL_OPT, label)

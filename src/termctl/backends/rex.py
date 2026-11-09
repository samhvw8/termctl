"""Superlogical Rex backend, written against its documented CLI.

UNTESTED against a real Rex: only argv mapping is covered, by a stub.
Docs: https://www.superlogical.com/rex/docs/reference/cli
JSON field names of `rex block ls --json` are not documented, so parsing is tolerant.
"""
from __future__ import annotations

import json
import os
import shlex
import shutil
import subprocess

from ..errors import BackendError, TargetError, UsageError
from ..pane import Pane
from ..proc import clean_title, tilde
from .base import WHERE, Backend, check_text, normalize_key, trim_screen

EXIT_TARGET = 3


def _first(d: dict, *keys, default=""):
    for k in keys:
        if d.get(k):
            return d[k]
    return default


def _block_id(obj) -> str:
    """The new block's ID from a creation result (window/session results nest it)."""
    if isinstance(obj, list):
        return _block_id(obj[0]) if obj else ""
    if not isinstance(obj, dict):
        return ""
    for key in ("block", "focused_block", "first_block"):
        v = obj.get(key)
        if isinstance(v, dict) and v.get("id"):
            return v["id"]
        if isinstance(v, str) and v:
            return v
    if isinstance(obj.get("blocks"), list) and obj["blocks"]:
        return _block_id(obj["blocks"][0])
    return obj.get("id", "")


class RexBackend(Backend):
    name = "rex"

    def _run(self, *args) -> str:
        try:
            r = subprocess.run(["rex", *args], capture_output=True, text=True)
        except FileNotFoundError:
            raise BackendError("rex is not installed") from None
        if r.returncode == EXIT_TARGET:
            raise TargetError(f"rex: target missing or ambiguous: {r.stderr.strip()}")
        if r.returncode != 0:
            raise BackendError(f"rex {args[0]}: exit {r.returncode}: {r.stderr.strip()}")
        return r.stdout

    def available(self) -> bool:
        return shutil.which("rex") is not None

    def _process(self, block: str) -> str:
        """Best effort: `rex block call process` returns a JSON process object."""
        try:
            d = json.loads(self._run("block", "call", "-b", block, "process"))
        except (TargetError, BackendError, ValueError):
            return ""
        if not isinstance(d, dict):
            return ""
        name = _first(d, "name", "command", "comm", "exe")
        name = str(name).rsplit("/", 1)[-1].split(" ", 1)[0]
        return name

    def list_panes(self) -> list[Pane]:
        data = json.loads(self._run("block", "ls", "-A", "--json") or "[]")
        blocks = data.get("blocks", []) if isinstance(data, dict) else data
        me = os.environ.get("REX_BLOCK", "")
        panes = []
        for b in blocks:
            native = str(_first(b, "id", "block_id"))
            proc = self._process(native)
            panes.append(Pane(
                id=f"rex:{native}", backend="rex", native=native,
                title=clean_title(str(_first(b, "title", "name"))),
                label=str(_first(b, "label")), process=proc,
                folder=tilde(str(_first(b, "cwd", "directory", "working_directory"))),
                is_self=bool(me) and native == me,
            ))
        return panes

    def self_pane(self):
        return next((p for p in self.list_panes() if p.is_self), None)

    def spawn(self, where, cmd, cwd=None, title=None) -> Pane:
        if where not in WHERE:
            raise UsageError(f"--where must be one of {', '.join(WHERE)}")
        opts = ["--json"] + (["-c", cwd] if cwd else [])
        if where == "tab":
            argv = ["window", "new", "--focus=false", *opts]
            if os.environ.get("REX_SESSION"):
                argv += ["-s", os.environ["REX_SESSION"]]
            if title:
                argv.append(title)
        elif where == "split":
            block = os.environ.get("REX_BLOCK", "")
            argv = ["split", "--focus=false", *opts] + (["-b", block] if block else [])
            if title:
                argv += ["--label", title]
        else:
            argv = ["new", *opts] + ([title] if title else [])
        if cmd:
            shell = os.environ.get("SHELL", "/bin/sh")
            argv += ["--shell", shell, "--interactive", "--", *shlex.split(cmd)]
        native = _block_id(json.loads(self._run(*argv) or "{}"))
        if not native:
            raise BackendError("rex: creation result carries no block ID (untested backend; check rex --json output)")
        return Pane(id=f"rex:{native}", backend="rex", native=native, title=title or "")

    def send_text(self, pane, text):
        check_text(text)
        self._run("send", "-b", pane.native, text)

    def send_key(self, pane, key):
        self._run("send-key", "-b", pane.native, normalize_key(key).replace("-", "+"))

    def capture(self, pane, lines=200):
        out = self._run("capture", "-b", pane.native, "--trim")
        return trim_screen(out.splitlines(), lines)

    def close(self, pane):
        self._run("block", "close", pane.native)

    def set_label(self, pane, label):
        self._run("block", "rename", pane.native, label)

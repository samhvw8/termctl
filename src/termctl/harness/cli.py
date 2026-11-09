"""harness-ops: agent-aware commands on top of the terminal layer."""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
from concurrent.futures import ThreadPoolExecutor

from ..backends import NAMES, detect, make
from ..backends.base import WHERE
from ..errors import TermctlError, UsageError
from ..resolve import resolve
from .ops import DEFAULT_TIMEOUT, Ops, harness_name

MSG_FLAGS = {"--force", "--clear-first", "--timeout", "--backend", "-L"}
SLASH_FLAGS = {"--force", "--wait", "--timeout", "--backend", "-L"}
LABEL_RE = re.compile(r"[A-Za-z0-9._-]{1,40}")


def _backend_name(args, target=None) -> str:
    if getattr(args, "backend", None):
        return args.backend
    if target and target.split(":", 1)[0] in NAMES and ":" in target:
        return target.split(":", 1)[0]
    name = detect()
    if name is None:
        raise UsageError("no terminal backend detected; pass --backend")
    return name


def open_backend(args, target=None):
    return make(_backend_name(args, target), socket=getattr(args, "socket", None))


def open_sender(args, backend):
    """The caller's pane: on the selected backend when it is ours, else on the env's backend."""
    name = detect()
    if name is None:
        return None
    if name == backend.name:
        return backend.self_pane()
    return make(name, socket=getattr(args, "socket", None) if name == "tmux" else None).self_pane()


def rows(panes):
    return [{
        "id": p.address, "pane_id": p.id, "tty": p.tty, "title": p.title, "label": p.label,
        "harness": harness_name(p), "folder": p.folder, "self": p.is_self, "status": p.status,
    } for p in panes]


def fill_status(ops: Ops, panes) -> None:
    with ThreadPoolExecutor(8) as ex:
        natives = list(ex.map(ops.backend.native_status, panes))
    for p, native in zip(panes, natives):
        p.status = ops.status(p, native)


def print_table(panes) -> None:
    def short(p):
        return p.address[:8] if p.backend == "iterm2" else p.address

    width = max([9] + [len(short(p)) + 1 for p in panes])
    print(f"{'ID':<{width}} {'HARNESS':<9} {'LABEL':<14} {'TITLE':<34} {'FOLDER':<36} STATUS")
    for p in panes:
        mark = " (you)" if p.is_self else ""
        print(f"{short(p):<{width}} {harness_name(p)[:9]:<9} {p.label[:14]:<14} "
              f"{(p.title[:28] + mark)[:34]:<34} {p.folder[:36]:<36} {p.status[:60]}")


def _common(parser, suppress=True):
    default = argparse.SUPPRESS if suppress else None
    parser.add_argument("--backend", choices=NAMES, default=default, help="terminal backend (default: detected)")
    parser.add_argument("-L", dest="socket", default=default, help="tmux server socket name")


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(prog="harness-ops", description="Talk to coding-agent panes.",
                                 epilog="Exit codes: 0 ok, 1 backend error, 2 refused target/usage, 4 draft on input line, 5 wait timed out.")
    _common(ap, suppress=False)
    sub = ap.add_subparsers(dest="cmd", required=True)

    def add(name, helptext, **kw):
        p = sub.add_parser(name, help=helptext, **kw)
        _common(p)
        return p

    p = add("ls", "list panes with harness, label, folder, status")
    p.add_argument("--json", action="store_true")
    p.add_argument("--all", action="store_true", help="merge every available backend")

    p = add("which", "resolve a target to one pane; never sends")
    p.add_argument("target")

    p = add("read", "print the last lines of a pane's screen", aliases=["capture"])
    p.add_argument("target")
    p.add_argument("-n", type=int, default=40, dest="lines")

    p = add("name", "label the current pane")
    p.add_argument("label")

    add("whoami", "print the current pane's row")

    p = add("msg", "send a tagged message", aliases=["send"])
    p.add_argument("--force", action="store_true", help="send even when the input line holds a draft")
    p.add_argument("--clear-first", action="store_true", help="run /clear, wait for idle, then send")
    p.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    p.add_argument("target")
    p.add_argument("text", nargs=argparse.REMAINDER, help="message text (put options before the target)")

    p = add("slash", "send a slash command on its own")
    p.add_argument("--force", action="store_true", help="wipe a draft first (the command must be alone on the line)")
    p.add_argument("--wait", action="store_true", help="wait for the harness to be idle afterwards")
    p.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    p.add_argument("target")
    p.add_argument("command", nargs=argparse.REMAINDER)

    p = add("clear", "send /clear and wait for idle")
    p.add_argument("--force", action="store_true")
    p.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    p.add_argument("target")

    p = add("wait-idle", "wait until a pane is idle")
    p.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    p.add_argument("target")

    p = add("spawn", "start a harness, wait until ready, submit the first prompt")
    p.add_argument("harness")
    p.add_argument("--where", choices=WHERE, default="tab")
    p.add_argument("--cwd", default=None)
    p.add_argument("--prompt", default=None)
    p.add_argument("--prompt-file", default=None)
    p.add_argument("--label", default=None)
    p.add_argument("--command", default=None, help="override the harness's start command")
    p.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    return ap


def _message_text(args) -> str:
    words = list(args.text)
    if getattr(args, "literal", False):
        return " ".join(words)
    stray = [w for w in words if w in MSG_FLAGS]
    if stray:
        raise UsageError(f"{stray[0]} must come before the target (to send it as text, put -- before the message)")
    return " ".join(words)


def run(args) -> int:
    cmd = args.cmd
    if cmd == "ls":
        names = [n for n in NAMES if n != "ghostty"] if args.all else [_backend_name(args)]
        panes = []
        for name in names:
            backend = open_backend(argparse.Namespace(**{**vars(args), "backend": name}))
            if args.all and not backend.available():
                continue
            ops = Ops(backend)
            found = backend.list_panes()
            fill_status(ops, found)
            panes += found
        if args.json:
            print(json.dumps(rows(panes), indent=1, ensure_ascii=False))
        else:
            print_table(panes)
        return 0

    target = getattr(args, "target", None)
    backend = open_backend(args, target)

    if cmd == "whoami":
        me = backend.self_pane()
        if me is None:
            raise UsageError("cannot tell which pane this runs in")
        fill_status(Ops(backend), [me])
        print_table([me])
        return 0
    if cmd == "name":
        if not LABEL_RE.fullmatch(args.label):
            raise UsageError("usage: name <label>  (letters, digits, . _ -; max 40)")
        me = backend.self_pane()
        if me is None:
            raise UsageError("cannot tell which pane this runs in")
        backend.set_label(me, args.label)
        print(f"labelled this pane: {args.label}")
        return 0
    if cmd == "which":
        pane = resolve(backend.list_panes(), target)
        fill_status(Ops(backend), [pane])
        print_table([pane])
        return 0
    if cmd in ("read", "capture"):
        pane = resolve(backend.list_panes(), target)
        print(f"--- {pane.address[:8]} {harness_name(pane)} {pane.title} {pane.folder} ---")
        print("\n".join(backend.capture(pane, args.lines)))
        return 0

    ops = Ops(backend, sender=open_sender(args, backend))
    if cmd == "wait-idle":
        pane = ops.pick(target)
        ops.wait_idle(pane, args.timeout)
        print(f"idle: {pane.address}")
        return 0
    if cmd in ("msg", "send"):
        pane = ops.pick(target)
        ops.msg(pane, _message_text(args), force=args.force, clear_first=args.clear_first, timeout=args.timeout)
        print(f"sent to {pane.address[:8]} ({harness_name(pane)}, {pane.title or pane.folder})")
        return 0
    if cmd == "slash":
        pane = ops.pick(target)
        stray = [w for w in args.command if w in SLASH_FLAGS] if not getattr(args, "literal", False) else []
        if stray:
            raise UsageError(f"{stray[0]} must come before the target (to send it as text, put -- before the command)")
        ops.slash(pane, " ".join(args.command), force=args.force, wait=args.wait, timeout=args.timeout)
        print(f"sent to {pane.address[:8]} ({harness_name(pane)}, {pane.title or pane.folder})")
        return 0
    if cmd == "clear":
        pane = ops.pick(target)
        ops.clear(pane, force=args.force, timeout=args.timeout)
        print(f"cleared {pane.address[:8]}")
        return 0
    if cmd == "spawn":
        pane = ops.spawn(args.harness, args.where, args.cwd or os.getcwd(), prompt=args.prompt,
                         prompt_file=args.prompt_file, label=args.label, command=args.command, timeout=args.timeout)
        print(pane.id)
        return 0
    raise UsageError(f"unknown command '{cmd}'")


def _split_double_dash(argv: list[str]) -> tuple[list[str], list[str] | None]:
    """argparse drops `--` inconsistently, so take the literal tail ourselves."""
    if "--" not in argv:
        return argv, None
    i = argv.index("--")
    return argv[:i], argv[i + 1 :]


def _attach_literal(args, tail: list[str] | None) -> None:
    if tail is None:
        return
    field = "text" if hasattr(args, "text") else "command" if hasattr(args, "command") and args.cmd == "slash" else None
    if field is None:
        if tail:
            raise UsageError(f"{args.cmd} takes no text after --")
        return
    existing = getattr(args, field)
    if existing:  # `--` inside the message is part of the message
        setattr(args, field, existing + ["--"] + tail)
    else:
        setattr(args, field, tail)
        args.literal = True


def main(argv=None) -> int:
    parser = build_parser()
    argv = list(sys.argv[1:] if argv is None else argv)
    head, tail = _split_double_dash(argv)
    try:
        args = parser.parse_args(head)
    except SystemExit as e:
        return int(e.code or 0)
    try:
        _attach_literal(args, tail)
        return run(args)
    except TermctlError as e:
        print(f"harness-ops: {e}", file=sys.stderr)
        return e.code
    except BrokenPipeError:
        return 0


def entry() -> None:
    sys.exit(main())


if __name__ == "__main__":
    entry()

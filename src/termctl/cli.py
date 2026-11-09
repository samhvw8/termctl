"""termctl: drive terminal panes through a backend. Knows nothing about harnesses."""
from __future__ import annotations

import argparse
import json
import re
import sys

from . import __version__
from .backends import NAMES, detect, make
from .backends.base import WHERE
from .errors import TargetError, TermctlError, UsageError
from .pane import Pane
from .resolve import resolve

LABEL_RE = re.compile(r"[A-Za-z0-9._-]{1,40}")


def _backend_name(args, target=None) -> str:
    if args.backend:
        return args.backend
    if target and ":" in target and target.split(":", 1)[0] in NAMES:
        return target.split(":", 1)[0]
    name = detect()
    if name is None:
        raise UsageError("no terminal backend detected; pass --backend")
    return name


def open_backend(args, target=None):
    return make(_backend_name(args, target), socket=args.socket)


def print_panes(panes: list[Pane]) -> None:
    width = max([9] + [len(p.address[:8] if p.backend == "iterm2" else p.address) + 1 for p in panes])
    print(f"{'ID':<{width}} {'PROCESS':<12} {'LABEL':<14} {'TITLE':<34} FOLDER")
    for p in panes:
        pid = p.address[:8] if p.backend == "iterm2" else p.address
        mark = " (you)" if p.is_self else ""
        print(f"{pid:<{width}} {p.process[:12]:<12} {p.label[:14]:<14} {(p.title[:28] + mark)[:34]:<34} {p.folder}")


def pick(backend, target: str, refuse_self: bool) -> Pane:
    pane = resolve(backend.list_panes(), target)
    if refuse_self and pane.is_self:
        raise TargetError("refusing to act on your own pane")
    return pane


def text_arg(words: list[str]) -> str:
    return " ".join(words[1:] if words[:1] == ["--"] else words)


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(prog="termctl", description="Drive terminal panes through backends.",
                                 epilog="Exit codes: 0 ok, 1 backend error, 2 refused target or usage.")
    ap.add_argument("--version", action="version", version=__version__)
    ap.add_argument("--backend", choices=NAMES, default=None)
    ap.add_argument("-L", dest="socket", default=None, help="tmux server socket name")
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("ls", help="list panes")
    p.add_argument("--json", action="store_true")
    p.add_argument("--all", action="store_true", help="merge every available backend")
    p = sub.add_parser("which", help="resolve a target to one pane")
    p.add_argument("target")
    p = sub.add_parser("spawn", help="open a pane running CMD")
    p.add_argument("--where", choices=WHERE, default="tab")
    p.add_argument("--cwd", default=None)
    p.add_argument("--title", default=None)
    p.add_argument("cmd_words", nargs=argparse.REMAINDER, metavar="CMD")
    p = sub.add_parser("send-text", help="type text literally, without Enter")
    p.add_argument("target")
    p.add_argument("text", nargs=argparse.REMAINDER)
    p = sub.add_parser("send-key", help="press keys: enter, escape, tab, ctrl-c, ctrl-u, ...")
    p.add_argument("target")
    p.add_argument("keys", nargs="+")
    p = sub.add_parser("capture", help="print the last lines of a pane's screen")
    p.add_argument("target")
    p.add_argument("-n", type=int, default=40, dest="lines")
    p = sub.add_parser("close", help="close a pane")
    p.add_argument("target")
    p = sub.add_parser("name", help="label the current pane")
    p.add_argument("label")
    sub.add_parser("whoami", help="print the current pane")
    return ap


def run(args) -> int:
    if args.cmd == "ls":
        names = [n for n in NAMES if n != "ghostty"] if args.all else [_backend_name(args)]
        panes: list[Pane] = []
        for name in names:
            b = make(name, socket=args.socket)
            if args.all and not b.available():
                continue
            panes += b.list_panes()
        if args.json:
            print(json.dumps([vars(p) | {"address": p.address} for p in panes], indent=1, ensure_ascii=False))
        else:
            print_panes(panes)
        return 0

    target = getattr(args, "target", None)
    backend = open_backend(args, target)
    if args.cmd == "whoami":
        me = backend.self_pane()
        if me is None:
            raise UsageError("cannot tell which pane this runs in")
        print_panes([me])
    elif args.cmd == "name":
        if not LABEL_RE.fullmatch(args.label):
            raise UsageError("usage: name <label>  (letters, digits, . _ -; max 40)")
        me = backend.self_pane()
        if me is None:
            raise UsageError("cannot tell which pane this runs in")
        backend.set_label(me, args.label)
        print(f"labelled this pane: {args.label}")
    elif args.cmd == "which":
        print_panes([pick(backend, target, refuse_self=False)])
    elif args.cmd == "spawn":
        words = args.cmd_words[1:] if args.cmd_words[:1] == ["--"] else args.cmd_words
        print(backend.spawn(args.where, " ".join(words) or None, cwd=args.cwd, title=args.title).id)
    elif args.cmd == "send-text":
        backend.send_text(pick(backend, target, refuse_self=True), text_arg(args.text))
    elif args.cmd == "send-key":
        pane = pick(backend, target, refuse_self=True)
        for key in args.keys:
            backend.send_key(pane, key)
    elif args.cmd == "capture":
        print("\n".join(backend.capture(pick(backend, target, refuse_self=False), args.lines)))
    elif args.cmd == "close":
        backend.close(pick(backend, target, refuse_self=True))
    else:
        raise UsageError(f"unknown command '{args.cmd}'")
    return 0


def main(argv=None) -> int:
    ap = build_parser()
    try:
        args = ap.parse_args(argv)
    except SystemExit as e:
        return int(e.code or 0)
    try:
        return run(args)
    except TermctlError as e:
        print(f"termctl: {e}", file=sys.stderr)
        return e.code
    except BrokenPipeError:
        return 0


def entry() -> None:
    sys.exit(main())


if __name__ == "__main__":
    entry()

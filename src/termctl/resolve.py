"""Target resolution. The first rule with matches wins."""
from __future__ import annotations

from pathlib import Path

from .errors import TargetError
from .pane import Pane


def _short(p: Pane) -> str:
    return p.address[:8] if p.backend == "iterm2" else p.address


def resolve(panes: list[Pane], target: str) -> Pane:
    t = target.strip().lower()
    rules = [
        ("id", lambda p: p.id.lower() == t or (len(t) >= 4 and p.native.lower() == t)),
        ("label", lambda p: p.label.lower() == t),
        ("title", lambda p: p.title.lower() == t),
        ("folder", lambda p: bool(p.folder) and Path(p.folder).name.lower() == t),
        ("title contains", lambda p: t in p.title.lower()),
        ("id prefix", lambda p: len(t) >= 4 and (p.native.lower().startswith(t) or p.id.lower().startswith(t))),
    ]
    for rule, ok in rules:
        hits = [p for p in panes if ok(p)]
        if len(hits) == 1:
            return hits[0]
        if len(hits) > 1:
            listing = "\n".join(f"  {_short(p):<9} {p.process:<8} {p.title[:40]:<40} {p.folder}" for p in hits)
            raise TargetError(f"'{target}' matches {len(hits)} panes by {rule}; use a label or an ID prefix:\n{listing}")
    raise TargetError(f"no pane matches '{target}'. Run: ls")

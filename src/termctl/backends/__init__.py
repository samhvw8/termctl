"""Backend registry and selection from the environment."""
from __future__ import annotations

import os
from pathlib import Path

from ..errors import UsageError
from .base import Backend

NAMES = ("iterm2", "tmux", "rex", "ghostty")
ITERM_APP = Path("/Applications/iTerm.app")
_iterm2_instance: Backend | None = None


def detect(env=None, iterm_installed: bool | None = None) -> str | None:
    """The backend of the pane we run in. tmux first: it is the innermost when nested."""
    env = os.environ if env is None else env
    if env.get("TMUX"):
        return "tmux"
    if env.get("REX_SESSION"):
        return "rex"
    if env.get("ITERM_SESSION_ID"):
        return "iterm2"
    installed = ITERM_APP.exists() if iterm_installed is None else iterm_installed
    return "iterm2" if installed else None


def _iterm2() -> Backend:
    """One per process: iTerm's library keeps a global App tied to the first connection."""
    global _iterm2_instance
    if _iterm2_instance is None:
        from .iterm2_backend import ITerm2Backend

        _iterm2_instance = ITerm2Backend()
    return _iterm2_instance


def make(name: str, socket: str | None = None, socket_path: str | None = None) -> Backend:
    if name == "iterm2":
        return _iterm2()
    if name == "tmux":
        from .tmux import TmuxBackend

        return TmuxBackend(socket=socket, socket_path=socket_path)
    if name == "rex":
        from .rex import RexBackend

        return RexBackend()
    if name == "ghostty":
        from .ghostty import GhosttyBackend

        return GhosttyBackend()
    raise UsageError(f"unknown backend '{name}'. Known: {', '.join(NAMES)}")

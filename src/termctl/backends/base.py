"""The backend interface: everything Layer 2 is allowed to ask of a terminal."""
from __future__ import annotations

import re
from abc import ABC, abstractmethod

from ..errors import UsageError
from ..pane import Pane

KEY_NAMES = ("enter", "escape", "tab", "backspace", "up", "down", "left", "right")
WHERE = ("tab", "split", "window")


def normalize_key(key: str) -> str:
    k = key.strip().lower()
    if k in KEY_NAMES or re.fullmatch(r"ctrl-[a-z]", k):
        return k
    raise UsageError(f"unknown key '{key}'. Use one of: {', '.join(KEY_NAMES)}, ctrl-<letter>")


def check_text(text: str) -> None:
    """send_text never submits: a line break would act as Enter."""
    if "\n" in text or "\r" in text:
        raise UsageError("text must not contain line breaks; send_text never submits (use send_key enter)")


class Backend(ABC):
    name = ""

    @abstractmethod
    def list_panes(self) -> list[Pane]: ...

    @abstractmethod
    def spawn(self, where: str, cmd: str | None, cwd: str | None = None, title: str | None = None) -> Pane: ...

    @abstractmethod
    def send_text(self, pane: Pane, text: str) -> None: ...

    @abstractmethod
    def send_key(self, pane: Pane, key: str) -> None: ...

    @abstractmethod
    def capture(self, pane: Pane, lines: int = 200) -> list[str]: ...

    @abstractmethod
    def close(self, pane: Pane) -> None: ...

    @abstractmethod
    def set_label(self, pane: Pane, label: str) -> None: ...

    @abstractmethod
    def self_pane(self) -> Pane | None: ...

    def native_status(self, pane: Pane) -> str | None:
        """Busy/idle reported by the terminal itself, or None when it has no such channel."""
        return None

    def available(self) -> bool:
        return True


def trim_screen(lines: list[str], n: int) -> list[str]:
    lines = [ln.replace("\x00", " ").rstrip() for ln in lines]  # iTerm2 gives NUL for cells a TUI never wrote
    while lines and not lines[-1]:
        lines.pop()
    return lines[-n:]

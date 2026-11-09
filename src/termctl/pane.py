from __future__ import annotations

from dataclasses import dataclass


@dataclass
class Pane:
    id: str  # `<backend>:<native>`
    backend: str
    native: str
    tty: str = ""
    title: str = ""
    label: str = ""
    process: str = ""  # main program on the pane (a harness if one runs there)
    folder: str = ""
    is_self: bool = False
    status: str = ""

    @property
    def address(self) -> str:
        """The ID to print and to put in message tags.

        iTerm2 session IDs are UUIDs and stand alone, so agent-msg tags keep their
        historic form. Every other backend needs the prefix to be routable.
        """
        return self.native if self.backend == "iterm2" else self.id

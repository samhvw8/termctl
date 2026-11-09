"""Ghostty stub. Fails loudly so nobody mistakes it for a working backend."""
from __future__ import annotations

from ..errors import BackendError
from .base import Backend

MSG = "not supported: Ghostty has no control API yet"


class GhosttyBackend(Backend):
    name = "ghostty"

    def _no(self):
        raise BackendError(MSG)

    def list_panes(self):
        self._no()

    def spawn(self, where, cmd, cwd=None, title=None):
        self._no()

    def send_text(self, pane, text):
        self._no()

    def send_key(self, pane, key):
        self._no()

    def capture(self, pane, lines=200):
        self._no()

    def close(self, pane):
        self._no()

    def set_label(self, pane, label):
        self._no()

    def self_pane(self):
        self._no()

    def available(self):
        return False

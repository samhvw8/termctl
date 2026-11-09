"""The task registry: one JSON file, atomic writes, one exclusive lock around each update."""
from __future__ import annotations

import fcntl
import json
import os
import tempfile
from contextlib import contextmanager
from pathlib import Path

from ..errors import BackendError


def default_root() -> Path:
    return Path(os.environ.get("TERMCTL_STATE_DIR") or Path.home() / ".local" / "state" / "termctl")


def _empty() -> dict:
    return {"version": 1, "tasks": {}, "panes": {}, "pending": []}


class Registry:
    def __init__(self, root: Path | str | None = None):
        self.root = Path(root) if root else default_root()
        self.path = self.root / "registry.json"

    def read(self) -> dict:
        """A consistent snapshot: writers replace the file atomically, so no lock is needed."""
        try:
            return json.loads(self.path.read_text())
        except FileNotFoundError:
            return _empty()
        except ValueError as e:
            # Resetting would silently forget running tasks.
            raise BackendError(f"task registry is corrupt: {self.path}: {e}") from e

    @contextmanager
    def update(self):
        """Hold the lock, yield the data, and write it back only if the block succeeds."""
        self.root.mkdir(parents=True, exist_ok=True)
        with open(self.root / "registry.lock", "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            data = self.read()
            yield data
            self._write(data)

    def _write(self, data: dict) -> None:
        fd, tmp = tempfile.mkstemp(dir=self.root, prefix=".registry-")
        try:
            with os.fdopen(fd, "w") as f:
                json.dump(data, f, indent=1, ensure_ascii=False)
                f.flush()
                os.fsync(f.fileno())
            os.replace(tmp, self.path)
        except BaseException:
            os.unlink(tmp)
            raise

import pytest


@pytest.fixture(autouse=True)
def _private_state_dir(tmp_path, monkeypatch):
    """No test may touch the real task registry."""
    monkeypatch.setenv("TERMCTL_STATE_DIR", str(tmp_path / "state"))

import json
import subprocess
import sys
import textwrap

import pytest

from termctl.errors import BackendError
from termctl.harness.registry import Registry


def test_read_of_missing_registry_creates_nothing(tmp_path):
    root = tmp_path / "none"
    assert Registry(root).read()["tasks"] == {}
    assert not root.exists()


def test_update_persists(tmp_path):
    r = Registry(tmp_path)
    with r.update() as d:
        d["tasks"]["a"] = {"x": 1}
    assert Registry(tmp_path).read()["tasks"]["a"]["x"] == 1


def test_failed_update_writes_nothing(tmp_path):
    r = Registry(tmp_path)
    with pytest.raises(ValueError):
        with r.update() as d:
            d["tasks"]["a"] = {"x": 1}
            raise ValueError
    assert r.read()["tasks"] == {}


def test_corrupt_registry_is_an_error_not_a_reset(tmp_path):
    r = Registry(tmp_path)
    with r.update():
        pass
    r.path.write_text("{not json")
    with pytest.raises(BackendError) as e:
        r.read()
    assert str(r.path) in str(e.value)
    assert r.path.read_text() == "{not json"


WRITER = textwrap.dedent("""
    import sys
    from termctl.harness.registry import Registry
    r = Registry(sys.argv[1])
    for i in range(int(sys.argv[3])):
        with r.update() as d:
            d["tasks"].setdefault("c", {"n": 0})
            d["tasks"]["c"]["n"] += 1
            d["tasks"][f"{sys.argv[2]}-{i}"] = {}
""")


def test_two_writers_lose_nothing(tmp_path):
    n = 60
    procs = [subprocess.Popen([sys.executable, "-c", WRITER, str(tmp_path), name, str(n)]) for name in "ab"]
    assert [p.wait(timeout=60) for p in procs] == [0, 0]
    data = json.loads(Registry(tmp_path).path.read_text())
    assert data["tasks"]["c"]["n"] == 2 * n
    assert len(data["tasks"]) == 2 * n + 1

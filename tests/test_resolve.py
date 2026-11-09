import pytest

from termctl.errors import TargetError
from termctl.resolve import resolve
from tests.fakes import make_pane


def panes():
    return [
        make_pane(1, label="alpha", title="Build things", folder="~/w/one"),
        make_pane(2, label="", title="Docs writer", folder="~/w/two"),
        make_pane(3, label="", title="Docs reader", folder="~/w/three"),
        make_pane(4, label="", title="alpha", folder="~/w/four"),
    ]


def test_label_wins_over_title():
    assert resolve(panes(), "alpha").native == "1"


def test_exact_title_case_insensitive():
    assert resolve(panes(), "docs writer").native == "2"


def test_folder_basename():
    assert resolve(panes(), "three").native == "3"


def test_title_substring_unique():
    assert resolve(panes(), "writ").native == "2"


def test_title_substring_ambiguous_lists_candidates():
    with pytest.raises(TargetError) as e:
        resolve(panes(), "docs")
    assert e.value.code == 2 and "matches 2" in str(e.value) and "Docs writer" in str(e.value)


def test_no_match_exit_2():
    with pytest.raises(TargetError) as e:
        resolve(panes(), "nope")
    assert e.value.code == 2


def test_id_prefix_needs_4_chars():
    ps = [make_pane(n) for n in (1, 2)]
    ps[0].native = "ABCDEF12"
    ps[0].id = "fake:ABCDEF12"
    ps[1].native = "ZZZZ9999"
    ps[1].id = "fake:ZZZZ9999"
    assert resolve(ps, "abcd").native == "ABCDEF12"
    with pytest.raises(TargetError):
        resolve(ps, "abc")


def test_full_pane_id_exact():
    assert resolve(panes(), "fake:3").native == "3"


def test_title_contains_before_id_prefix():
    ps = [make_pane(1, title="abcd tools"), make_pane(2)]
    ps[1].native = "abcd9999"
    ps[1].id = "fake:abcd9999"
    assert resolve(ps, "abcd").native == "1"

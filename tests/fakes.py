"""An in-memory backend and clock for unit tests."""
from __future__ import annotations

from termctl.backends.base import Backend
from termctl.pane import Pane


class FakeClock:
    def __init__(self):
        self.t = 0.0

    def now(self):
        return self.t

    def sleep(self, s):
        self.t += s


class FakePane:
    def __init__(self, input=""):
        self.input = input
        self.busy = 0  # captures left before the prompt returns
        self.log = ["some earlier output"]
        self.prompt = "> "


class FakeBackend(Backend):
    name = "fake"

    def __init__(self, panes: list[Pane], me: Pane | None = None, inputs: dict | None = None, busy_after_clear=3):
        self.panes = panes
        self.me = me
        self.state = {p.id: FakePane((inputs or {}).get(p.id, "")) for p in panes}
        self.events: list[tuple] = []
        self.busy_after_clear = busy_after_clear
        self.keep_clear_text = False  # simulate an Enter that the harness ignored

    def list_panes(self):
        return list(self.panes)

    def self_pane(self):
        return self.me

    def spawn(self, where, cmd, cwd=None, title=None):
        p = Pane(id=f"fake:{len(self.panes)}", backend="fake", native=str(len(self.panes)), title=title or "", folder=cwd or "", process="sh")
        self.panes.append(p)
        self.state[p.id] = FakePane()
        self.events.append(("spawn", where, cmd, cwd, title))
        return p

    def send_text(self, pane, text):
        st = self.state[pane.id]
        self.events.append(("text", pane.id, text, st.busy))
        st.input += text

    def send_key(self, pane, key):
        st = self.state[pane.id]
        self.events.append(("key", pane.id, key, st.busy))
        if key == "enter":
            line, st.input = st.input, ""
            if line == "/clear":
                if self.keep_clear_text:
                    st.input = line
                else:
                    st.busy = self.busy_after_clear
        elif key == "ctrl-u":
            st.input = ""

    def capture(self, pane, lines=200):
        st = self.state[pane.id]
        self.events.append(("capture", pane.id))
        if st.busy > 0:
            st.busy -= 1
            return st.log + [f"working {st.busy}"]
        return st.log + [f"{st.prompt}{st.input}"]

    def close(self, pane):
        self.events.append(("close", pane.id))

    def set_label(self, pane, label):
        self.events.append(("label", pane.id, label))
        pane.label = label

    # helpers for assertions
    def inputs_sent(self, pane_id=None):
        return [e for e in self.events if e[0] in ("text", "key") and (pane_id is None or e[1] == pane_id)]


def make_pane(n, label="", title="", folder="", process="python3", backend="fake"):
    return Pane(id=f"fake:{n}", backend=backend, native=str(n), label=label, title=title, folder=folder, process=process)

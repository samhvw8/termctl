package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	api "github.com/samhvw8/termctl/internal/iterm2api"
	"github.com/samhvw8/termctl/internal/term"
	"google.golang.org/protobuf/proto"
)

type tabState struct{ root, hidden []string }

func itermTabs(t *testing.T, c *api.Client) map[string]tabState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Call(ctx, &api.ClientOriginatedMessage{ListSessionsRequest: &api.ListSessionsRequest{}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]tabState{}
	var walk func(n *api.SplitTreeNode, ids *[]string)
	walk = func(n *api.SplitTreeNode, ids *[]string) {
		for _, l := range n.GetLinks() {
			if s := l.GetSession(); s != nil {
				*ids = append(*ids, s.GetUniqueIdentifier())
			} else {
				walk(l.GetNode(), ids)
			}
		}
	}
	for _, w := range resp.GetListSessionsResponse().GetWindows() {
		for _, tab := range w.GetTabs() {
			var st tabState
			walk(tab.GetRoot(), &st.root)
			for _, s := range tab.GetMinimizedSessions() {
				st.hidden = append(st.hidden, s.GetUniqueIdentifier())
			}
			out[tab.GetTabId()] = st
		}
	}
	return out
}

// toggleMaximize makes session the active pane of its tab and window, then selects View >
// Maximize Active Pane, which acts on the key window's active pane (a toggle).
func toggleMaximize(t *testing.T, c *api.Client, session string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.Call(ctx, &api.ClientOriginatedMessage{ActivateRequest: &api.ActivateRequest{
		SessionId: proto.String(session), OrderWindowFront: proto.Bool(true), SelectTab: proto.Bool(true), SelectSession: proto.Bool(true),
	}}); err != nil {
		t.Fatal(err)
	}
	// The item is disabled until the activated window is key; that can take a moment, and never
	// happens while another app is in front.
	var st api.MenuItemResponse_Status
	for i := 0; i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		resp, err := c.Call(ctx, &api.ClientOriginatedMessage{MenuItemRequest: &api.MenuItemRequest{Identifier: proto.String("Maximize Active Pane")}})
		if err != nil {
			t.Fatal(err)
		}
		if st = resp.GetMenuItemResponse().GetStatus(); st == api.MenuItemResponse_OK {
			time.Sleep(500 * time.Millisecond)
			return
		}
	}
	t.Fatalf("Maximize Active Pane: %s (is iTerm2 the active app?)", st)
}

// TestMaximizedPaneOnITerm2 is opt-in (TERMCTL_ITERM_TEST=1). It opens one scratch tab of three
// panes and maximizes one of them (View > Maximize Active Pane). The two panes hidden behind it
// must stay listed and reachable: ls, which, read, msg and wait-idle. Every other tab's state is
// checked before and after, so a maximize that lands elsewhere is undone at once and fails the test.
// Un-maximizes and closes everything it opened.
func TestMaximizedPaneOnITerm2(t *testing.T) {
	if os.Getenv("TERMCTL_ITERM_TEST") != "1" {
		t.Skip("set TERMCTL_ITERM_TEST=1")
	}
	tmp := t.TempDir()
	log := filepath.Join(tmp, "harness.log")
	b, err := term.Make("iterm2", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var made []*term.Pane
	t.Cleanup(func() {
		for _, p := range made {
			b.Close(p)
		}
	})
	a1, err := b.Spawn("tab", "sleep 900", tmp, "termctl-max-a1")
	if err != nil {
		t.Fatal(err)
	}
	made = append(made, a1)
	split := func(cmd, name string) *term.Pane {
		t.Helper()
		p, err := b.(term.Anchored).SpawnAt(a1, "split", cmd, tmp, name)
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, p)
		return p
	}
	a2 := split(fakeCmd(log), "termctl-max-a2") // a harness, hidden once a1 is maximized
	a3 := split("sleep 900", "termctl-max-a3")
	label := "termctl-max-" + fmt.Sprint(os.Getpid())
	if err := b.SetLabel(a2, label); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fake prompt", func() bool {
		lines, err := b.Capture(a2, 40)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(lines, func(l string) bool { return strings.TrimSpace(l) == ">" })
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c, err := api.Connect(ctx, "termctl-e2e")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() }) // after the un-maximize below
	before := itermTabs(t, c)
	var tab string
	for id, st := range before {
		if slices.Contains(st.root, a1.Native) {
			tab = id
		}
	}
	if got := before[tab]; len(got.root) != 3 || len(got.hidden) != 0 {
		t.Fatalf("scratch tab %s before maximize: %+v", tab, got)
	}

	toggleMaximize(t, c, a1.Native)
	after := itermTabs(t, c)
	for id, st := range after {
		if id != tab && len(st.hidden) != len(before[id].hidden) {
			toggleMaximize(t, c, st.root[0]) // undo at once: the key window's active pane was not ours
			t.Fatalf("the maximize landed on tab %s, not on the scratch tab %s; undone", id, tab)
		}
	}
	t.Cleanup(func() { // runs before the panes close
		if len(itermTabs(t, c)[tab].hidden) > 0 {
			toggleMaximize(t, c, a1.Native)
		}
	})
	if got := after[tab]; len(got.root) != 1 || got.root[0] != a1.Native || len(got.hidden) != 2 {
		t.Fatalf("scratch tab %s after maximize: %+v, want a1 alone in root and two hidden", tab, got)
	}
	t.Logf("maximized: ListSessions root %v, minimized_sessions %v", after[tab].root, after[tab].hidden)

	run := func(args ...string) (string, error) {
		cmd := exec.Command(filepath.Join(binDir, "harness-ops"), append([]string{"--backend", "iterm2"}, args...)...)
		cmd.Env = append(os.Environ(), "TERMCTL_STATE_DIR="+filepath.Join(tmp, "state"), "ITERM_SESSION_ID=w0t0p0:"+a1.Native)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("ls", "--all", "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(out, err)
	}
	inTab := map[string]bool{}
	for _, r := range rows {
		if r["tab"] == tab {
			inTab[fmt.Sprint(r["id"])] = true
		}
	}
	for _, p := range []*term.Pane{a1, a2, a3} {
		if !inTab[p.Address()] {
			t.Errorf("ls --all does not list %s in the maximized tab %s (it lists %v)", p.Address(), tab, inTab)
		}
	}
	for _, args := range [][]string{{"which", label}, {"read", label, "-n", "5"}, {"wait-idle", label, "--timeout", "20"}} {
		if out, err := run(args...); err != nil {
			t.Errorf("%v on a hidden pane: %s %v", args, out, err)
		}
	}
	if out, err := run("msg", label, "behind", "the", "maximized", "pane"); err != nil {
		t.Errorf("msg to a hidden pane: %s %v", out, err)
	} else {
		waitFor(t, "message logged", func() bool { return len(readLog(log)) >= 1 })
		if l := readLog(log); !strings.HasSuffix(l[len(l)-1], "] behind the maximized pane") {
			t.Errorf("fake harness got %q", l)
		}
	}
	if st := itermTabs(t, c)[tab]; len(st.hidden) != 2 {
		t.Errorf("the commands un-maximized the tab: %+v", st)
	}
	// Layout changes refuse a maximized tab before anything changes: its split tree holds one pane.
	for _, args := range [][]string{{"arrange", "tab-of:" + a1.ID, "--layout", "even"}, {"swap", a2.ID, a3.ID}, {"move", a3.ID, "--to", "new"}} {
		cmd := exec.Command(filepath.Join(binDir, "termctl"), append([]string{"--backend", "iterm2"}, args...)...)
		cmd.Env = append(os.Environ(), "TERMCTL_STATE_DIR="+filepath.Join(tmp, "state"), "ITERM_SESSION_ID=w0t0p0:"+a1.Native)
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 || !strings.Contains(string(out), "maximized pane") {
			t.Errorf("termctl %v on a maximized tab: %s %v, want exit 2 naming the maximized pane", args, out, err)
		}
	}
	if st := itermTabs(t, c)[tab]; len(st.root) != 1 || len(st.hidden) != 2 {
		t.Errorf("a refused command changed the tab: %+v", st)
	}
}

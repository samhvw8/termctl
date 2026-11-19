package harness

import (
	"reflect"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

// spawnedPane starts a pane through the lead's spawn, so the lead owns it, running process.
func spawnedPane(t *testing.T, r *rig, process string) (*Tasks, *term.Pane) {
	t.Helper()
	lead := r.as(r.owner)
	lead.SenderRef = map[string]string{"pid": "111"}
	p, err := lead.Spawn("claude", "tab", "/w", SpawnOpts{Command: "claude", Label: "partner", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	p.Process = process
	return lead, p
}

func pressedKeys(r *rig, pane string) []string {
	var out []string
	for _, k := range r.b.Typed(pane) {
		if strings.HasPrefix(k, "key:") {
			out = append(out, strings.TrimPrefix(k, "key:"))
		}
	}
	return out
}

func TestKeysPressesEveryKeyAndJournalsThem(t *testing.T) {
	r := newRig(t)
	lead, p := spawnedPane(t, r, "claude")
	before := len(pressedKeys(r, p.ID))
	res, err := lead.Keys("partner", []string{"Shift+Tab", "down", "ALT-m", "option-x"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"shift-tab", "down", "alt-m", "alt-x"}
	if !reflect.DeepEqual(res.Keys, want) || res.Pane.ID != p.ID || !reflect.DeepEqual(pressedKeys(r, p.ID)[before:], want) {
		t.Fatal(res, pressedKeys(r, p.ID))
	}
	entries, _, _ := (&Journal{Root: r.reg.Root}).Read(Filter{})
	last := entries[len(entries)-1]
	if last.Event != "key" || last.Text != "shift-tab down alt-m alt-x" || last.To != p.Address() || last.From != r.owner.Address() {
		t.Fatalf("%+v", last)
	}
}

func TestKeysOnATaskWorker(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	if _, err := r.tasks.Keys(x.ID, []string{"escape"}, false); err != nil {
		t.Fatal(err)
	}
	entries, _, _ := (&Journal{Root: r.reg.Root}).Read(Filter{Task: x.ID})
	if last := entries[len(entries)-1]; last.Event != "key" || last.Task != x.ID {
		t.Fatalf("%+v", last)
	}
	if _, err := r.as(r.other).Keys(x.ID, []string{"escape"}, false); !isCode(err, term.CodeGuard) {
		t.Fatal("another pane pressed keys on a task it does not own:", err)
	}
}

func TestKeysRefusals(t *testing.T) {
	r := newRig(t)
	lead, p := spawnedPane(t, r, "omp") // omp is calibrated: its input line is read
	before := len(pressedKeys(r, p.ID))

	_, err := lead.Keys(r.owner.ID, []string{"enter"}, false)
	if !isCode(err, term.CodeUsage) || !strings.Contains(err.Error(), "your own pane") {
		t.Fatal("own pane:", err)
	}
	_, err = lead.Keys(r.other.ID, []string{"enter"}, false) // nobody started it
	if !isCode(err, term.CodeGuard) || !strings.Contains(err.Error(), "termctl send-key") {
		t.Fatal("not owned:", err)
	}
	elsewhere := r.as(r.owner) // the same pane ID on another server is another owner
	elsewhere.SenderRef = map[string]string{"pid": "222"}
	if _, err := elsewhere.Keys(p.ID, []string{"enter"}, false); !isCode(err, term.CodeGuard) {
		t.Fatal("another server:", err)
	}
	for _, bad := range [][]string{{"1"}, {"enter", "nonsense"}, {"ctrl-1"}, {"f13"}} {
		if _, err := lead.Keys("partner", bad, false); !isCode(err, term.CodeUsage) {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	r.b.State[p.ID].Input = "half typed"
	if _, err := lead.Keys("partner", []string{"enter"}, false); !isCode(err, term.CodeDraft) || !strings.Contains(err.Error(), "half typed") {
		t.Fatal("draft:", err)
	}
	if got := pressedKeys(r, p.ID)[before:]; len(got) != 0 {
		t.Fatalf("a refused key list pressed %q", got)
	}
	if _, err := lead.Keys("partner", []string{"ctrl-u"}, true); err != nil || r.b.State[p.ID].Input != "" {
		t.Fatal("--force:", err, r.b.State[p.ID].Input)
	}
}

func TestKeysRefusedOnAPermissionDialog(t *testing.T) {
	r := newRig(t)
	lead, p := spawnedPane(t, r, "claude")
	dlg := &termtest.Dialog{Body: "touch x", Options: []string{"Yes", "No"}, Selected: 1}
	r.b.State[p.ID].Dialog = dlg
	before := len(pressedKeys(r, p.ID))
	for _, force := range []bool{false, true} {
		_, err := lead.Keys("partner", []string{"down", "enter"}, force)
		if !isCode(err, term.CodeDialog) || !strings.Contains(err.Error(), "answer or deny") {
			t.Fatal(force, err)
		}
	}
	if got := pressedKeys(r, p.ID)[before:]; len(got) != 0 || dlg.Answer != 0 {
		t.Fatalf("keys reached a dialog: %q", got)
	}
}

// refusing is a backend that cannot send home: the list is refused before its first key.
type refusing struct{ *termtest.Fake }

func (refusing) CheckKey(key string) error {
	if key == "home" {
		return term.UsageErr("the refusing backend cannot send key 'home'")
	}
	return nil
}

func TestKeysCheckedAgainstTheBackendBeforeTheFirstIsSent(t *testing.T) {
	r := newRig(t)
	lead, p := spawnedPane(t, r, "claude")
	lead.Backend = refusing{r.b}
	lead.Factory = func(string, map[string]string) (term.Backend, error) { return refusing{r.b}, nil }
	before := len(pressedKeys(r, p.ID))
	if _, err := lead.Keys(p.ID, []string{"enter", "home"}, false); !isCode(err, term.CodeUsage) || !strings.Contains(err.Error(), "home") {
		t.Fatal(err)
	}
	if got := pressedKeys(r, p.ID)[before:]; len(got) != 0 {
		t.Fatalf("pressed %q before the refusal", got)
	}
}

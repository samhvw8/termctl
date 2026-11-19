package term

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeKeyGrammar(t *testing.T) {
	for in, want := range map[string]string{
		"Enter": "enter", "ESCAPE": "escape", "ctrl+C": "ctrl-c", "Ctrl-U": "ctrl-u", "alt+m": "alt-m", "ALT-7": "alt-7",
		"meta-x": "alt-x", "Option+X": "alt-x", "meta+1": "alt-1", "shift+tab": "shift-tab", "Shift-Tab": "shift-tab",
		"space": "space", "delete": "delete", "home": "home", "end": "end", "PageUp": "pageup", "pagedown": "pagedown",
		"f1": "f1", "F12": "f12", " up ": "up",
	} {
		if got, err := NormalizeKey(in); got != want || err != nil {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"1", "9", "0", "f0", "f13", "ctrl-1", "alt-", "alt-mm", "shift-a", "+", "nonsense-key", ""} {
		if _, err := NormalizeKey(bad); code(err) != CodeUsage {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
	_, err := NormalizeKey("nonsense")
	for _, name := range append(append([]string{}, keyNames...), "f1-f12", "ctrl-<letter>", "alt-<letter or digit>", "plain digit") {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the usage error does not name %s: %v", name, err)
		}
	}
}

// allKeys is every canonical key name a caller can give.
func allKeys() []string {
	keys := append([]string{}, keyNames...)
	for c := 'a'; c <= 'z'; c++ {
		keys = append(keys, "ctrl-"+string(c), "alt-"+string(c))
	}
	for c := '0'; c <= '9'; c++ {
		keys = append(keys, "alt-"+string(c))
	}
	for i := 1; i <= 12; i++ {
		keys = append(keys, "f"+itoa(i))
	}
	return keys
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return "1" + string(rune('0'+i-10))
}

func TestTmuxKeyNames(t *testing.T) {
	for key, want := range map[string]string{
		"enter": "Enter", "escape": "Escape", "tab": "Tab", "shift-tab": "BTab", "backspace": "BSpace", "delete": "DC",
		"space": "Space", "up": "Up", "down": "Down", "left": "Left", "right": "Right", "home": "Home", "end": "End",
		"pageup": "PPage", "pagedown": "NPage", "ctrl-c": "C-c", "alt-m": "M-m", "alt-1": "M-1", "f1": "F1", "f12": "F12",
	} {
		if got, err := tmuxKeyName(key); got != want || err != nil {
			t.Errorf("%s: %q %v", key, got, err)
		}
	}
	for _, k := range allKeys() {
		if err := (&TmuxBackend{}).CheckKey(k); err != nil {
			t.Errorf("tmux refuses %s: %v", k, err)
		}
	}
}

func TestITermKeyBytesForEveryKey(t *testing.T) {
	for key, want := range map[string]string{
		"enter": "\r", "escape": "\x1b", "tab": "\t", "shift-tab": "\x1b[Z", "backspace": "\x7f", "space": " ",
		"delete": "\x1b[3~", "home": "\x1b[H", "end": "\x1b[F", "pageup": "\x1b[5~", "pagedown": "\x1b[6~",
		"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
		"ctrl-a": "\x01", "ctrl-z": "\x1a", "alt-m": "\x1bm", "alt+1": "\x1b1", "meta-x": "\x1bx",
		"f1": "\x1bOP", "f2": "\x1bOQ", "f3": "\x1bOR", "f4": "\x1bOS", "f5": "\x1b[15~", "f6": "\x1b[17~",
		"f7": "\x1b[18~", "f8": "\x1b[19~", "f9": "\x1b[20~", "f10": "\x1b[21~", "f11": "\x1b[23~", "f12": "\x1b[24~",
	} {
		if got, err := iterm2KeyBytes(key); got != want || err != nil {
			t.Errorf("%s: %q %v", key, got, err)
		}
	}
	for _, k := range allKeys() {
		if err := (&ITerm2Backend{}).CheckKey(k); err != nil {
			t.Errorf("iterm2 refuses %s: %v", k, err)
		}
	}
}

func TestRexKeyNames(t *testing.T) {
	for key, want := range map[string]string{
		"enter": "enter", "escape": "escape", "shift-tab": "shift+tab", "pageup": "page_up", "pagedown": "page_down",
		"home": "home", "end": "end", "delete": "delete", "space": "space", "ctrl-c": "ctrl+c", "alt-m": "alt+m", "f5": "f5",
	} {
		if got, err := rexKeyName(key); got != want || err != nil {
			t.Errorf("%s: %q %v", key, got, err)
		}
	}
	for _, k := range allKeys() {
		if err := (&RexBackend{}).CheckKey(k); err != nil {
			t.Errorf("rex refuses %s: %v", k, err)
		}
	}
}

func TestHcKeyNamesAndRefusals(t *testing.T) {
	for key, want := range map[string]string{
		"enter": "enter", "escape": "esc", "tab": "tab", "backspace": "backspace", "up": "up", "ctrl-c": "ctrl+c",
		"alt-x": "alt+x", "meta-1": "alt+1", "f5": "f5", "f12": "f12",
	} {
		if got, err := hcKeyName(key); got != want || err != nil {
			t.Errorf("%s: %q %v", key, got, err)
		}
	}
	for _, k := range []string{"space", "delete", "home", "end", "pageup", "pagedown", "shift-tab"} {
		err := (&HcBackend{}).CheckKey(k)
		if code(err) != CodeUsage || !strings.Contains(err.Error(), "hc backend") || !strings.Contains(err.Error(), "'"+k+"'") {
			t.Errorf("hc must refuse %s by name: %v", k, err)
		}
	}
}

func TestCheckKeysRefusesTheListBeforeAnyKey(t *testing.T) {
	got, err := CheckKeys(&TmuxBackend{}, []string{"Alt+M", "shift+tab", "F5", "home"})
	if err != nil || !reflect.DeepEqual(got, []string{"alt-m", "shift-tab", "f5", "home"}) {
		t.Fatal(got, err)
	}
	if _, err := CheckKeys(&HcBackend{}, []string{"enter", "home"}); code(err) != CodeUsage {
		t.Fatal(err)
	}
	if _, err := CheckKeys(&TmuxBackend{}, []string{"enter", "2"}); code(err) != CodeUsage {
		t.Fatal(err)
	}
}

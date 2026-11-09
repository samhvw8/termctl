package cli

import "testing"

// A reply from an hc or tmux pane to an iTerm2 session ID must not go to the caller's own backend.
func TestBackendNameRoutesBareITermUUID(t *testing.T) {
	t.Setenv("HC_TERMINAL_ID", "t1")
	t.Setenv("TMUX", "")
	name, err := backendName(&Options{}, "F1B64CB3-AD81-41A9-85BB-F6348D55F86D")
	if err != nil || name != "iterm2" {
		t.Fatal(name, err)
	}
	if name, _ := backendName(&Options{}, "hc:t7"); name != "hc" {
		t.Fatal(name)
	}
	if name, _ := backendName(&Options{}, "worker-label"); name != "hc" {
		t.Fatal(name)
	}
	if name, _ := backendName(&Options{Backend: "tmux"}, "F1B64CB3-AD81-41A9-85BB-F6348D55F86D"); name != "tmux" {
		t.Fatal(name)
	}
}

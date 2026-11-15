package term

import (
	"encoding/json"
	"testing"
)

func specJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLayoutTreeShapes(t *testing.T) {
	ids := []string{"m", "b", "c", "d", "e"}
	cases := map[string]string{
		"columns":   `{"children":[{"session_id":"m"},{"session_id":"b"},{"session_id":"c"},{"session_id":"d"},{"session_id":"e"}],"vertical":true}`,
		"rows":      `{"children":[{"session_id":"m"},{"session_id":"b"},{"session_id":"c"},{"session_id":"d"},{"session_id":"e"}],"vertical":false}`,
		"main-left": `{"children":[{"session_id":"m"},{"children":[{"session_id":"b"},{"session_id":"c"},{"session_id":"d"},{"session_id":"e"}],"vertical":false}],"vertical":true}`,
		"main-top":  `{"children":[{"session_id":"m"},{"children":[{"session_id":"b"},{"session_id":"c"},{"session_id":"d"},{"session_id":"e"}],"vertical":true}],"vertical":false}`,
		// five panes tile as rows of ceil(sqrt(5)) = 3, as tmux's tiled does
		"tiled": `{"children":[{"children":[{"session_id":"m"},{"session_id":"b"},{"session_id":"c"}],"vertical":true},{"children":[{"session_id":"d"},{"session_id":"e"}],"vertical":true}],"vertical":false}`,
	}
	for layout, want := range cases {
		if got := specJSON(t, layoutTree(layout, ids)); got != want {
			t.Errorf("%s:\n got %s\nwant %s", layout, got, want)
		}
	}
}

// apply_layout refuses a splitter with one child and same-orientation nesting; small tabs must not produce either.
func TestLayoutTreeHasNoLoneOrNestedSplitters(t *testing.T) {
	if got := specJSON(t, layoutTree("main-left", []string{"m", "b"})); got != `{"children":[{"session_id":"m"},{"session_id":"b"}],"vertical":true}` {
		t.Fatal(got)
	}
	if got := specJSON(t, layoutTree("tiled", []string{"a", "b"})); got != `{"children":[{"session_id":"a"},{"session_id":"b"}],"vertical":true}` {
		t.Fatal(got)
	}
	if got := specJSON(t, layoutTree("tiled", []string{"a", "b", "c", "d", "e", "f", "g"})); got != `{"children":[{"children":[{"session_id":"a"},{"session_id":"b"},{"session_id":"c"}],"vertical":true},{"children":[{"session_id":"d"},{"session_id":"e"},{"session_id":"f"}],"vertical":true},{"session_id":"g"}],"vertical":false}` {
		t.Fatal(got)
	}
}

func TestLayoutSpecFlattensWhatApplyLayoutRefuses(t *testing.T) {
	// [a | [b | c]] has a side-by-side splitter inside another; a root with one link is that link.
	root := node(true, leaf("a", 10, 10), sub(node(true, leaf("b", 10, 10), leaf("c", 10, 10))))
	if got := specJSON(t, layoutSpec(root)); got != `{"children":[{"session_id":"a"},{"session_id":"b"},{"session_id":"c"}],"vertical":true}` {
		t.Fatal(got)
	}
	if got := specJSON(t, layoutSpec(node(false, leaf("solo", 10, 10)))); got != `{"session_id":"solo"}` {
		t.Fatal(got)
	}
	mixed := node(true, leaf("a", 10, 10), sub(node(false, leaf("b", 10, 5), leaf("c", 10, 5))))
	if got := specJSON(t, layoutSpec(mixed)); got != `{"children":[{"session_id":"a"},{"children":[{"session_id":"b"},{"session_id":"c"}],"vertical":false}],"vertical":true}` {
		t.Fatal(got)
	}
}

// A swap exchanges the two sessions and leaves every size with its place.
func TestSwapSessionsKeepsSizesByPlace(t *testing.T) {
	root := node(true, leaf("a", 120, 40), sub(node(false, leaf("b", 40, 15), leaf("c", 40, 25))))
	got := swapSessions(root, "a", "c")
	if s := sizes(got); s != "c=120x40 b=40x15 a=40x25" {
		t.Fatal(s)
	}
	if s := sizes(root); s != "a=120x40 b=40x15 c=40x25" {
		t.Fatal("the original tree changed:", s)
	}
}

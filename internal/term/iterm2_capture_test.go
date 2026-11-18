package term

import (
	"context"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	api "github.com/samhvw8/termctl/internal/iterm2api"
)

// A read that fits the pane asks iTerm2 for the screen; a read of more lines than the pane has rows asks
// for the last lines of the buffer (trailing_lines), which reach into the scrollback. iTerm2 refuses a
// LineRange with both set (INVALID_LINE_RANGE), so exactly one is. A pane of unknown height (hidden behind
// a maximized pane) is read from the screen only. Both are the same passive read (HistoryIsSafe).
func TestITerm2CaptureReadsTheScrollbackPastTheScreen(t *testing.T) {
	var asked []*api.LineRange
	conn := &fakeConn{handle: func(r *api.ClientOriginatedMessage) *api.ServerOriginatedMessage {
		asked = append(asked, r.GetGetBufferRequest().GetLineRange())
		var lines []*api.LineContents
		for _, s := range []string{"history 1", "history 2", "screen 1", "screen 2", "", "\x00\x00"} {
			lines = append(lines, &api.LineContents{Text: proto.String(s)})
		}
		return &api.ServerOriginatedMessage{GetBufferResponse: &api.GetBufferResponse{Contents: lines}}
	}}
	b := &ITerm2Backend{connect: func(context.Context) (itermConn, error) { return conn, nil }}
	p := &Pane{ID: "iterm2:S", Backend: "iterm2", Native: "S", Rows: 4}
	got, err := b.Capture(p, 4)
	if err != nil || !asked[0].GetScreenContentsOnly() || asked[0].TrailingLines != nil {
		t.Fatalf("a read that fits: %v %v", asked[0], err)
	}
	if !reflect.DeepEqual(got, []string{"history 1", "history 2", "screen 1", "screen 2"}) {
		t.Fatalf("%q", got)
	}
	got, err = b.Capture(p, 3)
	if err != nil || !asked[1].GetScreenContentsOnly() || !reflect.DeepEqual(got, []string{"history 2", "screen 1", "screen 2"}) {
		t.Fatalf("fewer lines than the screen: %v %q %v", asked[1], got, err)
	}
	if _, err := b.Capture(p, 2000); err != nil || asked[2].GetTrailingLines() != 2000 || asked[2].ScreenContentsOnly != nil {
		t.Fatalf("a history read: %v %v", asked[2], err)
	}
	if _, err := b.Capture(p, 5_000_000); err != nil || asked[3].GetTrailingLines() != maxTrailingLines {
		t.Fatalf("a history read is capped: %v %v", asked[3], err)
	}
	if _, err := b.Capture(&Pane{ID: "iterm2:H", Native: "H"}, 2000); err != nil || !asked[4].GetScreenContentsOnly() {
		t.Fatalf("a pane of unknown height: %v %v", asked[4], err)
	}
	if !b.HistoryIsSafe(p) {
		t.Fatal("iTerm2 reads its own copy of the scrollback: the history is safe")
	}
}

// ListPanes gives each pane its height in rows, so a screen read asks for no more than the screen.
func TestITerm2PanesKnowTheirRows(t *testing.T) {
	conn := &fakeConn{handle: func(r *api.ClientOriginatedMessage) *api.ServerOriginatedMessage {
		if r.ListSessionsRequest != nil {
			return &api.ServerOriginatedMessage{ListSessionsResponse: maximizedResponse()}
		}
		if v := r.GetVariableRequest(); v != nil {
			values := make([]string, len(v.GetGet()))
			for i := range values {
				values[i] = `"no-such-tty"`
			}
			return &api.ServerOriginatedMessage{VariableResponse: &api.VariableResponse{Values: values}}
		}
		return &api.ServerOriginatedMessage{}
	}}
	b := &ITerm2Backend{connect: func(context.Context) (itermConn, error) { return conn, nil }}
	panes, err := b.ListPanes()
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]int{}
	for _, p := range panes {
		rows[p.Native] = p.Rows
	}
	if want := map[string]int{"max": 50, "p1": 50, "p2": 50, "h1": 0, "h2": 0}; !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows %v, want %v", rows, want)
	}
}

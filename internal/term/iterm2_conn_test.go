package term

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	api "github.com/samhvw8/termctl/internal/iterm2api"
)

type fakeConn struct {
	fail   error
	closed bool
	calls  int
	handle func(*api.ClientOriginatedMessage) *api.ServerOriginatedMessage
}

func (c *fakeConn) Call(_ context.Context, req *api.ClientOriginatedMessage) (*api.ServerOriginatedMessage, error) {
	c.calls++
	if c.fail != nil {
		return nil, c.fail
	}
	if c.handle != nil {
		return c.handle(req), nil
	}
	return &api.ServerOriginatedMessage{}, nil
}

func (c *fakeConn) Close() error { c.closed = true; return nil }

// One broken call (a dropped websocket, a read timeout) must not break every later call: the next call
// connects again. An error iTerm2 answered with keeps the connection (review F11).
func TestITerm2ReconnectsAfterABrokenCall(t *testing.T) {
	var conns []*fakeConn
	b := &ITerm2Backend{connect: func(context.Context) (itermConn, error) {
		c := &fakeConn{}
		conns = append(conns, c)
		return c, nil
	}}
	if _, err := b.call(&api.ClientOriginatedMessage{}); err != nil {
		t.Fatal(err)
	}
	conns[0].fail = errors.New("failed to read frame header: EOF")
	if _, err := b.call(&api.ClientOriginatedMessage{}); err == nil {
		t.Fatal("the broken call succeeded")
	}
	if _, err := b.call(&api.ClientOriginatedMessage{}); err != nil || len(conns) != 2 || !conns[0].closed {
		t.Fatalf("after a broken call: err=%v connections=%d closed=%v", err, len(conns), conns[0].closed)
	}
	conns[1].fail = &api.ServerError{Msg: "no such session"}
	b.call(&api.ClientOriginatedMessage{})
	if len(conns) != 2 || conns[1].closed {
		t.Fatal("an answered error dropped a working connection")
	}
}

// A status read that fails for a session still alive (an iTerm2 older than 3.7.6) is an error, with
// the hint; one for a session that ended since the list is not (review F24).
func TestITerm2StatusErrorsOnlyForLiveSessions(t *testing.T) {
	failed := api.InvokeFunctionResponse_FAILED
	conn := &fakeConn{handle: func(r *api.ClientOriginatedMessage) *api.ServerOriginatedMessage {
		if r.ListSessionsRequest != nil {
			link := &api.SplitTreeNode_SplitTreeLink{Session: &api.SessionSummary{UniqueIdentifier: proto.String("ALIVE")}}
			return &api.ServerOriginatedMessage{ListSessionsResponse: &api.ListSessionsResponse{Windows: []*api.ListSessionsResponse_Window{{
				Tabs: []*api.ListSessionsResponse_Tab{{Root: &api.SplitTreeNode{Links: []*api.SplitTreeNode_SplitTreeLink{link}}}}}}}}
		}
		return &api.ServerOriginatedMessage{InvokeFunctionResponse: &api.InvokeFunctionResponse{
			Error: &api.InvokeFunctionResponse_Error{Status: &failed, ErrorReason: proto.String("no such function")}}}
	}}
	b := &ITerm2Backend{connect: func(context.Context) (itermConn, error) { return conn, nil }}
	gone := &Pane{ID: "iterm2:GONE", Native: "GONE"}
	if _, err := b.ProgramStatuses([]*Pane{gone}); err != nil {
		t.Fatal("an ended session is no error:", err)
	}
	_, err := b.ProgramStatuses([]*Pane{gone, {ID: "iterm2:ALIVE", Native: "ALIVE"}})
	if err == nil || !strings.Contains(err.Error(), "3.7.6") {
		t.Fatalf("a live session's failure was swallowed: %v", err)
	}
}

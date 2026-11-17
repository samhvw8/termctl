package term

import (
	"context"
	"errors"
	"testing"

	api "github.com/samhvw8/termctl/internal/iterm2api"
)

type fakeConn struct {
	fail   error
	closed bool
	calls  int
}

func (c *fakeConn) Call(context.Context, *api.ClientOriginatedMessage) (*api.ServerOriginatedMessage, error) {
	c.calls++
	if c.fail != nil {
		return nil, c.fail
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

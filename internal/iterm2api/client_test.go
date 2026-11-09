package iterm2api

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// fakeITerm listens on a real unix socket and checks the handshake iTerm2 checks.
func fakeITerm(t *testing.T, handle func(*ClientOriginatedMessage) *ServerOriginatedMessage) (string, *http.Header) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	headers := &http.Header{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*headers = r.Header.Clone()
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"api.iterm2.com"}, InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := context.Background()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var req ClientOriginatedMessage
			if proto.Unmarshal(data, &req) != nil {
				return
			}
			resp := handle(&req)
			resp.Id = req.Id
			out, _ := proto.Marshal(resp)
			c.Write(ctx, websocket.MessageBinary, out)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return path, headers
}

func TestHandshakeAndCall(t *testing.T) {
	path, headers := fakeITerm(t, func(r *ClientOriginatedMessage) *ServerOriginatedMessage {
		if r.ListSessionsRequest == nil {
			t.Error("expected list_sessions_request")
		}
		return &ServerOriginatedMessage{ListSessionsResponse: &ListSessionsResponse{}}
	})
	c, err := dial(context.Background(), path, "COOKIE", "KEY", "harness-ops")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	resp, err := c.Call(context.Background(), &ClientOriginatedMessage{ListSessionsRequest: &ListSessionsRequest{}})
	if err != nil || resp.ListSessionsResponse == nil {
		t.Fatal(resp, err)
	}
	for k, want := range map[string]string{"Origin": "ws://localhost/", "X-Iterm2-Cookie": "COOKIE", "X-Iterm2-Key": "KEY",
		"X-Iterm2-Advisory-Name": "harness-ops", "X-Iterm2-Disable-Auth-Ui": "true"} {
		if got := headers.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.HasPrefix(headers.Get("X-Iterm2-Library-Version"), "python ") {
		t.Errorf("library version %q", headers.Get("X-Iterm2-Library-Version"))
	}
	if headers.Get("Sec-Websocket-Protocol") != "api.iterm2.com" {
		t.Errorf("subprotocol %q", headers.Get("Sec-Websocket-Protocol"))
	}
}

func TestServerErrorIsReturned(t *testing.T) {
	path, _ := fakeITerm(t, func(*ClientOriginatedMessage) *ServerOriginatedMessage {
		return &ServerOriginatedMessage{Error: proto.String("nope")}
	})
	c, err := dial(context.Background(), path, "", "", "x")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Call(context.Background(), &ClientOriginatedMessage{ListSessionsRequest: &ListSessionsRequest{}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatal(err)
	}
}

func TestIDsMatchRequests(t *testing.T) {
	path, _ := fakeITerm(t, func(*ClientOriginatedMessage) *ServerOriginatedMessage { return &ServerOriginatedMessage{} })
	c, _ := dial(context.Background(), path, "", "", "x")
	defer c.Close()
	r1, _ := c.Call(context.Background(), &ClientOriginatedMessage{})
	r2, _ := c.Call(context.Background(), &ClientOriginatedMessage{})
	if r1.GetId() == r2.GetId() || r1.GetId() == 0 {
		t.Fatal(r1.GetId(), r2.GetId())
	}
}

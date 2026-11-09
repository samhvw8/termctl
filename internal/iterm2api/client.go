// Package iterm2api speaks iTerm2's API: protobuf messages over a WebSocket on a unix socket.
// api.proto holds only the messages termctl uses.
package iterm2api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// libraryVersion is what we claim to iTerm2; its minimum-version check keys off this.
const libraryVersion = "python 2.30"

// ErrUnauthorized means iTerm2 refused the cookie.
var ErrUnauthorized = errors.New("iTerm2 refused the connection")

type Client struct {
	conn *websocket.Conn
	next int64
}

func socketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	suite := os.Getenv("IT2_SUITE")
	if suite == "" {
		suite = "iTerm2"
	}
	return filepath.Join(home, "Library", "Application Support", suite, "private", "socket"), nil
}

func dial(ctx context.Context, path, cookie, key, name string) (*Client, error) {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	h := http.Header{}
	h.Set("Origin", "ws://localhost/")
	h.Set("X-Iterm2-Library-Version", libraryVersion)
	h.Set("X-Iterm2-Disable-Auth-Ui", "true")
	h.Set("X-Iterm2-Advisory-Name", name)
	if cookie != "" {
		h.Set("X-Iterm2-Cookie", cookie)
	}
	if key != "" {
		h.Set("X-Iterm2-Key", key)
	}
	conn, resp, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: tr}, HTTPHeader: h, Subprotocols: []string{"api.iterm2.com"},
	})
	if err != nil {
		if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 406) {
			return nil, fmt.Errorf("%w (HTTP %d)", ErrUnauthorized, resp.StatusCode)
		}
		return nil, err
	}
	conn.SetReadLimit(-1) // screen contents can be large
	return &Client{conn: conn}, nil
}

func requestCookie(ctx context.Context, name string) (string, string, error) {
	app := `application "iTerm2"`
	if p := os.Getenv("IT2_APP_PATH"); p != "" {
		app = fmt.Sprintf(`application %q`, p)
	}
	script := fmt.Sprintf(`tell %s to request cookie and key for app named %q`, app, name)
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("osascript: %s", strings.TrimSpace(string(out)))
	}
	parts := strings.Fields(string(out))
	if len(parts) != 2 {
		return "", "", fmt.Errorf("osascript: unexpected cookie reply")
	}
	return parts[0], parts[1], nil
}

// Connect opens the API. A cookie from the environment is tried first, then a fresh one.
func Connect(ctx context.Context, name string) (*Client, error) {
	path, err := socketPath()
	if err != nil {
		return nil, err
	}
	cookie, key := os.Getenv("ITERM2_COOKIE"), os.Getenv("ITERM2_KEY")
	if cookie != "" {
		c, err := dial(ctx, path, cookie, key, name)
		if err == nil || !errors.Is(err, ErrUnauthorized) {
			return c, err
		}
	}
	cookie, key, err = requestCookie(ctx, name)
	if err != nil {
		return nil, err
	}
	return dial(ctx, path, cookie, key, name)
}

// Call sends one request and waits for the response with its ID.
func (c *Client) Call(ctx context.Context, req *ClientOriginatedMessage) (*ServerOriginatedMessage, error) {
	c.next++
	req.Id = proto.Int64(c.next)
	data, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		return nil, err
	}
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		var resp ServerOriginatedMessage
		if err := proto.Unmarshal(data, &resp); err != nil {
			return nil, err
		}
		if resp.GetId() != c.next {
			continue // a notification, or a late reply
		}
		if resp.Error != nil {
			return nil, errors.New(resp.GetError())
		}
		return &resp, nil
	}
}

func (c *Client) Close() error { return c.conn.CloseNow() }

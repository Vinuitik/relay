// Package acp is a minimal Agent Client Protocol client: JSON-RPC 2.0 over
// an agent subprocess's stdin/stdout, one JSON object per line. ACP is what
// lets one runner drive Claude Code (via the claude-agent-acp adapter),
// Codex (codex-acp), Gemini CLI (gemini --acp), ... the same way - see
// https://agentclientprotocol.com and runner/FLOWS.md "Sessions (ACP)".
//
// Only the client side the runner needs is implemented: initialize,
// session/new, session/set_mode, session/prompt, session/cancel, plus
// receiving session/update notifications and session/request_permission
// requests. The client advertises no fs/terminal capabilities, so the agent
// uses its own built-in file and shell tools.
package acp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// ProtocolVersion is the ACP major version this client speaks.
const ProtocolVersion = 1

// ErrClosed is returned by Call for requests still pending when the agent's
// stdout closes (process exited or was killed).
var ErrClosed = errors.New("acp: agent connection closed")

// Handlers receive agent-initiated traffic. Both are called from the
// client's single reader goroutine, so they must not block on the agent
// replying to anything (a permission request is answered later, via
// Client.Respond, from whichever goroutine gets the user's decision).
type Handlers struct {
	// OnNotification gets every notification, e.g. "session/update".
	OnNotification func(method string, params json.RawMessage)
	// OnRequest gets every agent->client request, e.g.
	// "session/request_permission". It must eventually answer with c.Respond
	// or c.RespondError using id, or the agent waits forever.
	OnRequest func(c *Client, id json.RawMessage, method string, params json.RawMessage)
}

// Client is one connection to one agent subprocess.
type Client struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	handlers Handlers
	stderr   *tailBuffer

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse
	closed  bool
	done    chan struct{}
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("acp error %d: %s", e.Code, e.Message) }

// Start launches the agent command in dir and begins reading its output.
// The caller owns the process lifecycle from here: Cmd().Wait() to observe
// exit, Cmd().Process.Kill() to stop it.
func Start(name string, args []string, dir string, h Handlers) (*Client, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout pipe: %w", err)
	}
	c := &Client{
		cmd:      cmd,
		stdin:    stdin,
		handlers: h,
		stderr:   &tailBuffer{max: 4096},
		pending:  map[int64]chan rpcResponse{},
		done:     make(chan struct{}),
	}
	// The agent logs to stderr; keep the tail so a failed start can say why.
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	go c.readLoop(stdout)
	return c, nil
}

// Cmd is the underlying agent process.
func (c *Client) Cmd() *exec.Cmd { return c.cmd }

// Done is closed once the agent's stdout has closed (the process exited or
// was killed) and every pending Call has been failed with ErrClosed.
func (c *Client) Done() <-chan struct{} { return c.done }

// StderrTail returns the last few KB the agent wrote to stderr.
func (c *Client) StderrTail() string { return c.stderr.String() }

// Call sends a request and blocks until its response arrives (or the
// connection closes). result may be nil to discard the result.
func (c *Client) Call(method string, params any, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	id := c.nextID
	c.nextID++
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	rawID, _ := json.Marshal(id)
	if err := c.write(rpcMessage{ID: rawID, Method: method, Params: mustMarshal(params)}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	resp := <-ch
	if resp.err != nil {
		return resp.err
	}
	if result != nil && len(resp.result) > 0 {
		if err := json.Unmarshal(resp.result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

// Notify sends a notification (no response expected).
func (c *Client) Notify(method string, params any) error {
	return c.write(rpcMessage{Method: method, Params: mustMarshal(params)})
}

// Respond answers an agent->client request.
func (c *Client) Respond(id json.RawMessage, result any) error {
	return c.write(rpcMessage{ID: id, Result: mustMarshal(result)})
}

// RespondError answers an agent->client request with an error.
func (c *Client) RespondError(id json.RawMessage, code int, message string) error {
	return c.write(rpcMessage{ID: id, Error: &RPCError{Code: code, Message: message}})
}

func (c *Client) write(m rpcMessage) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write to agent: %w", err)
	}
	return nil
}

func (c *Client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	// Tool results (file contents, command output) can make single lines big.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m rpcMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue // not JSON-RPC (stray log line) - ignore
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			if c.handlers.OnRequest != nil {
				c.handlers.OnRequest(c, m.ID, m.Method, m.Params)
			} else {
				_ = c.RespondError(m.ID, -32601, "method not supported: "+m.Method)
			}
		case m.Method != "":
			if c.handlers.OnNotification != nil {
				c.handlers.OnNotification(m.Method, m.Params)
			}
		default:
			var id int64
			if err := json.Unmarshal(m.ID, &id); err != nil {
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if !ok {
				continue
			}
			if m.Error != nil {
				ch <- rpcResponse{err: m.Error}
			} else {
				ch <- rpcResponse{result: m.Result}
			}
		}
	}
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		ch <- rpcResponse{err: ErrClosed}
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

func mustMarshal(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("acp: marshal %T: %v", v, err))
	}
	return b
}

// tailBuffer is an io.Writer keeping only the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

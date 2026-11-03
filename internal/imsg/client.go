// Package imsg speaks JSON-RPC 2.0 to one long-lived `imsg rpc` child over its stdio.
package imsg

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Error is a JSON-RPC error returned by imsg.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if len(e.Data) == 0 {
		return fmt.Sprintf("imsg: %s (%d)", e.Message, e.Code)
	}
	return fmt.Sprintf("imsg: %s (%d): %s", e.Message, e.Code, e.Data)
}

// ErrClosed is returned for calls made after the child exited.
var ErrClosed = errors.New("imsg: rpc child exited")

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// NotificationHandler receives every notification the child sends, such as watch's "message".
type NotificationHandler func(method string, params json.RawMessage)

// Client owns one `imsg rpc` child.
type Client struct {
	log    zerolog.Logger
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	notify NotificationHandler

	writeLock sync.Mutex
	lock      sync.Mutex
	nextID    int64
	pending   map[int64]chan response
	stderrEOF chan struct{}
	exited    chan struct{}
	exitErr   error
}

// Start spawns `<path> rpc` and begins reading its stdout.
func Start(log zerolog.Logger, path string, notify NotificationHandler) (*Client, error) {
	cmd := exec.Command(path, "rpc")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s rpc: %w", path, err)
	}
	c := &Client{
		log:       log,
		cmd:       cmd,
		stdin:     stdin,
		notify:    notify,
		pending:   make(map[int64]chan response),
		stderrEOF: make(chan struct{}),
		exited:    make(chan struct{}),
	}
	go c.logStderr(stderr)
	go c.read(stdout)
	return c, nil
}

// Exited is closed once the child is gone; Err then says why.
func (c *Client) Exited() <-chan struct{} { return c.exited }

// Err is the reason the child exited, or nil while it runs.
func (c *Client) Err() error {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.exitErr
}

// Call sends one request and decodes its result into result, which may be nil. Each call is
// logged with its outcome and duration, under ctx's logger when it has one.
func (c *Client) Call(ctx context.Context, method string, params, result any) (err error) {
	start := time.Now()
	defer func() {
		log := zerolog.Ctx(ctx)
		if log.GetLevel() == zerolog.Disabled {
			log = &c.log
		}
		entry := log.Info()
		if err != nil && !errors.Is(err, context.Canceled) {
			entry = log.Warn().Err(err)
			if rpcErr, ok := errors.AsType[*Error](err); ok {
				entry = entry.Int("code", rpcErr.Code)
			}
		}
		if p, ok := params.(map[string]any); ok {
			if chat, ok := p["chat_guid"].(string); ok {
				entry = entry.Str("chat_guid", chat)
			}
		}
		entry.Str("event", "imsg_call").Str("method", method).Bool("ok", err == nil).
			Int64("duration_ms", time.Since(start).Milliseconds()).Msg("imsg call")
	}()
	c.lock.Lock()
	if c.exitErr != nil {
		c.lock.Unlock()
		return ErrClosed
	}
	c.nextID++
	id := c.nextID
	reply := make(chan response, 1)
	c.pending[id] = reply
	c.lock.Unlock()

	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		c.forget(id)
		return err
	}
	c.writeLock.Lock()
	_, err = c.stdin.Write(append(request, '\n'))
	c.writeLock.Unlock()
	if err != nil {
		c.forget(id)
		return fmt.Errorf("imsg: write %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		// The child still runs the request; its eventual response is dropped.
		c.forget(id)
		return ctx.Err()
	case <-c.exited:
		return ErrClosed
	case r := <-reply:
		if r.Error != nil {
			return r.Error
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(r.Result, result)
	}
}

func (c *Client) forget(id int64) {
	c.lock.Lock()
	delete(c.pending, id)
	c.lock.Unlock()
}

func (c *Client) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		var frame struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			response
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			c.log.Error().Err(err).Msg("Undecodable line from imsg rpc")
			continue
		}
		if frame.ID == nil {
			c.notify(frame.Method, frame.Params)
			continue
		}
		c.lock.Lock()
		reply, ok := c.pending[*frame.ID]
		delete(c.pending, *frame.ID)
		c.lock.Unlock()
		if ok {
			reply <- frame.response
		}
	}
	// Wait closes the pipes, so stderr must be drained first or the child's last words are lost.
	<-c.stderrEOF
	err := c.cmd.Wait()
	if err == nil {
		err = scanner.Err()
	}
	if err == nil {
		err = ErrClosed
	}
	c.lock.Lock()
	c.exitErr = err
	c.lock.Unlock()
	close(c.exited)
}

func (c *Client) logStderr(stderr io.Reader) {
	defer close(c.stderrEOF)
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		c.log.Warn().Str("stderr", scanner.Text()).Msg("imsg rpc")
	}
}

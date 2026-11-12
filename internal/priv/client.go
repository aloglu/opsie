package priv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNeedsPassword means sudo wants a password before it will start the
// helper. Running `sudo -v` in the terminal caches it.
var ErrNeedsPassword = errors.New("sudo needs your password")

// ErrHelperGone means the helper exited.
var ErrHelperGone = errors.New("the admin helper stopped")

// Client talks to a running helper. It's safe for concurrent use.
type Client struct {
	cmd *exec.Cmd
	in  io.WriteCloser

	wmu sync.Mutex

	pmu     sync.Mutex
	pending map[uint64]chan response
	closed  bool

	next atomic.Uint64
	dead chan struct{}
}

// Start launches the helper with `sudo -n` and waits for it to answer.
func Start(ctx context.Context) (*Client, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return StartExe(ctx, exe)
}

// StartExe starts the helper from a given opsie binary, such as one that
// was just installed by an update.
func StartExe(ctx context.Context, exe string) (*Client, error) {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return nil, errors.New("sudo isn't installed")
	}
	cmd := exec.Command("sudo", "-n", "--", exe, "helper")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("couldn't run sudo: %w", err)
	}
	c := &Client{cmd: cmd, in: in, pending: map[uint64]chan response{}, dead: make(chan struct{})}
	go c.read(out)

	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.Call(pctx, "ping", nil, nil); err != nil {
		c.Close()
		<-c.dead
		msg := strings.TrimSpace(stderr.String())
		switch {
		case strings.Contains(msg, "password is required"), strings.Contains(msg, "terminal is required"):
			return nil, ErrNeedsPassword
		case strings.Contains(msg, "not in the sudoers"), strings.Contains(msg, "not allowed to execute"):
			return nil, errors.New("your account isn't allowed to use sudo")
		case msg != "":
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return c, nil
}

func (c *Client) read(out io.Reader) {
	dec := json.NewDecoder(out)
	for {
		var r response
		if err := dec.Decode(&r); err != nil {
			break
		}
		c.pmu.Lock()
		ch, ok := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.pmu.Unlock()
		if ok {
			ch <- r
		}
	}
	_ = c.cmd.Wait()
	c.pmu.Lock()
	c.closed = true
	c.pmu.Unlock()
	close(c.dead)
}

// Alive reports whether the helper is still running.
func (c *Client) Alive() bool {
	if c == nil {
		return false
	}
	select {
	case <-c.dead:
		return false
	default:
		return true
	}
}

// Call runs op with args and decodes its answer into out (which may be nil).
func (c *Client) Call(ctx context.Context, op string, args map[string]string, out any) error {
	id := c.next.Add(1)
	ch := make(chan response, 1)
	c.pmu.Lock()
	if c.closed {
		c.pmu.Unlock()
		return ErrHelperGone
	}
	c.pending[id] = ch
	c.pmu.Unlock()
	forget := func() {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
	}

	line, err := json.Marshal(request{ID: id, Op: op, Args: args})
	if err != nil {
		forget()
		return err
	}
	c.wmu.Lock()
	_, err = c.in.Write(append(line, '\n'))
	c.wmu.Unlock()
	if err != nil {
		forget()
		return ErrHelperGone
	}

	select {
	case r := <-ch:
		if r.Err != "" {
			return errors.New(r.Err)
		}
		if out != nil && len(r.Data) > 0 {
			return json.Unmarshal(r.Data, out)
		}
		return nil
	case <-ctx.Done():
		forget()
		return ctx.Err()
	case <-c.dead:
		return ErrHelperGone
	}
}

// Close stops the helper by closing its input.
func (c *Client) Close() {
	c.wmu.Lock()
	_ = c.in.Close()
	c.wmu.Unlock()
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() > 64*1024 {
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

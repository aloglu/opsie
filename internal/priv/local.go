package priv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
)

// Caller answers helper requests, whether through the sudo helper or, in
// the background service that already runs as root, directly.
type Caller interface {
	Call(ctx context.Context, op string, args map[string]string, out any) error
	Alive() bool
}

// Local runs operations in this process, which must be root.
type Local struct{}

// Alive reports whether this process can run them.
func (Local) Alive() bool { return os.Geteuid() == 0 }

// Call runs op, decoding its result into out like Client.Call does.
func (Local) Call(ctx context.Context, op string, args map[string]string, out any) error {
	resp := handle(request{Op: op, Args: args})
	if resp.Err != "" {
		return errors.New(resp.Err)
	}
	if out != nil && resp.Data != nil {
		return json.Unmarshal(resp.Data, out)
	}
	return nil
}

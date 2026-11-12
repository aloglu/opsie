package priv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// opTimeout bounds an operation. Pulling and building images can take a
// long time on a slow connection; everything else is quick.
func opTimeout(op string) time.Duration {
	switch op {
	case "compose.update", "compose.up", "backup.create", "backup.restore", "backup.verify", "apt.autoremove", "disk.usage", "apt.update", "apt.upgrade", "apt.auto-on", "fail2ban.install", "ufw.install":
		return 30 * time.Minute
	}
	return 3 * time.Minute
}

// Serve answers requests from in until it closes, which happens when the
// dashboard exits. Requests run concurrently.
func Serve(in io.Reader, out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("the helper runs as root; opsie starts it with sudo")
	}
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	var (
		wmu sync.Mutex
		wg  sync.WaitGroup
	)
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := handle(req)
			wmu.Lock()
			_ = enc.Encode(resp)
			wmu.Unlock()
		}()
	}
	wg.Wait()
	return nil
}

func handle(req request) (resp response) {
	resp.ID = req.ID
	defer func() {
		if r := recover(); r != nil {
			resp.Err = fmt.Sprintf("helper crashed running %s: %v", req.Op, r)
		}
	}()
	op, ok := ops[req.Op]
	if !ok {
		resp.Err = fmt.Sprintf("unknown operation %q", req.Op)
		return resp
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout(req.Op))
	defer cancel()
	data, err := op(ctx, req.Args)
	if err != nil {
		resp.Err = err.Error()
		return resp
	}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			resp.Err = err.Error()
			return resp
		}
		resp.Data = raw
	}
	return resp
}

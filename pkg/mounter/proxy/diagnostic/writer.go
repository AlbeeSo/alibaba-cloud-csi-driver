package diagnostic

import (
	"bytes"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

const maxLine = 64 << 10

// Writer buffers complete lines so credentials split across Write calls stay private.
// Oversized lines are discarded rather than emitting an unclassified partial secret.
type Writer struct {
	mu      sync.Mutex
	target  atomic.Pointer[io.Writer]
	secrets map[string]string
	pending []byte
	discard bool
}

func NewWriter(target io.Writer, secrets map[string]string) *Writer {
	copied := make(map[string]string, len(secrets))
	for key, value := range secrets {
		copied[key] = value
	}
	w := &Writer{secrets: copied}
	w.target.Store(&target)
	return w
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		size := len(p)
		if end >= 0 {
			size = end + 1
		}
		if !w.discard {
			if len(w.pending)+size > maxLine {
				w.pending = nil
				w.discard = true
			} else {
				w.pending = append(w.pending, p[:size]...)
			}
		}
		p = p[size:]
		if end >= 0 {
			if err := w.flushLocked(); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func (w *Writer) flushLocked() error {
	message := string(w.pending)
	if w.discard {
		message = "[oversized client diagnostic omitted]\n"
	}
	w.pending = nil
	w.discard = false
	if message == "" {
		return nil
	}
	_, err := io.WriteString(*w.target.Load(), Redact(message, w.secrets))
	return err
}

func (w *Writer) Flush() error { w.mu.Lock(); defer w.mu.Unlock(); return w.flushLocked() }

// Routing changes must not wait for a blocked container log sink.
func (w *Writer) SwitchTarget(target io.Writer) { w.target.Store(&target) }

func FlushCommand(cmd *exec.Cmd) {
	for _, output := range []io.Writer{cmd.Stdout, cmd.Stderr} {
		if buffered, ok := output.(interface{ Flush() error }); ok {
			_ = buffered.Flush()
		}
	}
}

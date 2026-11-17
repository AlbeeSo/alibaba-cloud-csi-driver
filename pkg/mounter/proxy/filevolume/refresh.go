package filevolume

import (
	"fmt"
	"os"
	"path/filepath"
)

// Read one directory snapshot per Pod rather than scanning in every worker.
// This requires coherent host/guest directory reads: it does not invalidate a
// runtime's stale virtiofs cache or guarantee visibility of a host unlink.
func (r *Runner) refreshControls() error {
	entries, err := os.ReadDir(filepath.Join(r.Root, "desired"))
	if os.IsNotExist(err) {
		err = nil
	}
	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	r.controls, r.controlErr = names, err
	return err
}

func (r *Runner) readDesired(key string) (Desired, error) {
	r.controlMu.RLock()
	present, err := r.controls[ID(key)+".json"], r.controlErr
	r.controlMu.RUnlock()
	if err != nil {
		return Desired{}, fmt.Errorf("control directory unavailable")
	}
	if !present {
		return Desired{}, os.ErrNotExist
	}
	var d Desired
	err = ReadJSON(RequestPath(r.Root, key), &d)
	return d, err
}

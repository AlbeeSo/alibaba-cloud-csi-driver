package filevolume

import (
	"fmt"
	"os"
	"path/filepath"
)

type readySession struct {
	PodUID  string `json:"podUID"`
	Session string `json:"session"`
}

// Persist the startup barrier before a probe returns success. Existing app
// containers can retain bind mounts of the old FUSE instance across a sidecar
// restart, so successful remounting in the sidecar is not transparent recovery.
func markReady(root, uid, session string) error {
	var old readySession
	path := filepath.Join(root, "ready-session.json")
	if err := ReadJSON(path, &old); err == nil {
		if old.PodUID != uid || old.Session != session {
			return fmt.Errorf("Pod recreation required after mountproxy restart")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return WriteJSON(path, readySession{PodUID: uid, Session: session})
}

func wasReady(root, uid string) (bool, error) {
	var old readySession
	if err := ReadJSON(filepath.Join(root, "ready-session.json"), &old); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if old.PodUID != uid || old.Session == "" {
		return false, fmt.Errorf("invalid ready session identity")
	}
	return true, nil
}

package relaycore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func discardable(status string) bool {
	return status == "completed" || status == "cancelled" || status == "rejected"
}

// cleanupStagingLocked runs only after a successful durable state commit (or
// loading one). Snapshot paths remain historical metadata. A failed or resumable
// job, a worker that has not returned from Engine.Send, or a manifest being
// prepared pins every overlapping source until its owner releases it.
func (c *Client) cleanupStagingLocked() {
	root := filepath.Join(c.stateDir, "staging")
	protected := make([]string, 0)
	for id, j := range c.state.Jobs {
		_, active := c.cancels[id]
		if j.Transfer.Direction == "send" && (active || !discardable(j.Transfer.Status)) {
			protected = append(protected, j.Transfer.Paths...)
		}
	}
	for _, paths := range c.preparing {
		protected = append(protected, paths...)
	}
	candidates := make(map[string]bool)
	for _, j := range c.state.Jobs {
		if j.Transfer.Direction == "send" && discardable(j.Transfer.Status) {
			for _, path := range j.Transfer.Paths {
				candidates[path] = true
			}
		}
	}
	for path := range candidates {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == root || !within(root, path) {
			continue
		}
		pinned := false
		for _, other := range protected {
			if within(path, other) || within(other, path) {
				pinned = true
				break
			}
		}
		if pinned {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			c.noticeLocked(fmt.Errorf("clean staged copy: %w", err))
			continue
		}
		if resolved != path {
			c.noticeLocked(errors.New("staged copy cleanup skipped a symlink path"))
			continue
		}
		// RemoveAll removes a nested symlink itself, never its target. The selected
		// path and all of its ancestors were checked above in app-private storage.
		if err = os.RemoveAll(path); err != nil {
			c.noticeLocked(fmt.Errorf("clean staged copy: %w", err))
			continue
		}
		// Native imports use one private batch directory. Remove empty ancestors,
		// stopping at the staging root or any directory still containing a source.
		for parent := filepath.Dir(path); parent != root && within(root, parent); parent = filepath.Dir(parent) {
			if err = os.Remove(parent); err != nil {
				break
			}
		}
	}
}

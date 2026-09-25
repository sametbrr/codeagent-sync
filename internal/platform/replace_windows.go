//go:build windows

package platform

import (
	"os"
	"time"
)

// replaceFile renames from over to. Windows refuses the rename while another
// process (often the agent itself) has the target open, so it is retried for
// about a second before giving up.
func replaceFile(from, to string) error {
	var err error
	for attempt := 1; attempt <= 10; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt) * 20 * time.Millisecond)
	}
	return err
}

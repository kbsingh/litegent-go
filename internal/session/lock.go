package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// processLocks prevents two goroutines in the same process from opening
// the same session. flock handles cross-process exclusion.
var processLocks sync.Map // map[string]struct{}

// Lock is an exclusive advisory lock held for the duration of a run.
type Lock struct {
	file *os.File
	dir  string
}

func acquireLock(dir string) (*Lock, error) {
	if _, loaded := processLocks.LoadOrStore(dir, struct{}{}); loaded {
		return nil, fmt.Errorf("session is already in use")
	}

	path := filepath.Join(dir, LockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		processLocks.Delete(dir)
		return nil, fmt.Errorf("opening session lock %q: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		processLocks.Delete(dir)
		return nil, fmt.Errorf("session is already in use: %w", err)
	}
	return &Lock{file: f, dir: dir}, nil
}

// Release unlocks and closes the lock. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	processLocks.Delete(l.dir)
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

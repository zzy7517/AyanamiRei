package tasktree

import (
	"log/slog"
	"sort"
	"sync"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// taskLocks serializes this process's work on one task directory. Keyed by the
// directory with symlinks resolved, so two spellings of one task directory
// share a lock.
var taskLocks sync.Map // resolved task directory -> *sync.Mutex

// lockTask takes the task directory's mutex and returns its release func.
func lockTask(taskDir string) func() {
	v, _ := taskLocks.LoadOrStore(realish(taskDir), &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// lockRepositories takes the cross-process git lock of every repository once,
// in a fixed order so that two operations on the same repositories cannot
// deadlock. Each repository is named by any working tree of it. The returned
// func releases them all; nothing is left locked when one cannot be taken.
func lockRepositories(repos []*repository, logger *slog.Logger) (func(), error) {
	ordered := make([]*repository, len(repos))
	copy(ordered, repos)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].common < ordered[j].common })

	var unlocks []func()
	for _, repo := range ordered {
		unlock, err := execenv.LockGitRoot(repo.root, logger)
		if err != nil {
			for i := len(unlocks) - 1; i >= 0; i-- {
				unlocks[i]()
			}
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}, nil
}

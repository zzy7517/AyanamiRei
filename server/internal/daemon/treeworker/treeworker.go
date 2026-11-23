// Package treeworker keeps the task trees on this machine in step with what
// the server says must exist.
//
// The server is the source of truth. Its work list names the trees to build
// and the ones that have to be gone, its mature list the trees of tasks that
// that have been complete long enough. Every tick reconciles the disk with
// them and remembers nothing in between, so a tick interrupted anywhere
// leaves a state the next one finishes: trees are built by tasktree.Create,
// which is safe to repeat, and a tree is removed only when a work item names
// it.
//
// A task's layout depends on all of its pairs, so the worker reads a task's
// whole pair list before it creates or removes one of its trees.
package treeworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/tasktree"
)

// What a work item asks for.
const (
	// ActionCreate asks for a tree to exist.
	ActionCreate = "create"
	// ActionRemove asks for a tree to be gone.
	ActionRemove = "remove"
)

// The states a tree has on the server.
const (
	StatePending = "pending"
	StateReady   = "ready"
	StateFailed  = "failed"
	StateDeleted = "deleted"
)

// WorkItem is one tree the server wants built or removed.
type WorkItem struct {
	TreeID  string `json:"tree_id"`
	IssueID string `json:"issue_id"`
	// TaskKey is the task's identifier as a directory name: what
	// tasktree.TaskKey makes of it.
	TaskKey     string `json:"task_key"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	BaseBranch  string `json:"base_branch"`
	// TaskBranch is the branch the tree was last reported on, "" when it
	// never was.
	TaskBranch string `json:"task_branch"`
	// Action is ActionCreate or ActionRemove.
	Action    string `json:"action"`
	LocalPath string `json:"local_path"`
}

// Tree is a pair's tree as the server stores it.
type Tree struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Path       string `json:"path"`
	TaskBranch string `json:"task_branch"`
	Error      string `json:"error"`
}

// Pair is one project and branch a task works on.
type Pair struct {
	ProjectID  string `json:"project_id"`
	BaseBranch string `json:"base_branch"`
	Position   int32  `json:"position"`
	// Tree is the pair's tree as the server stores it.
	Tree *Tree `json:"tree"`
	// LocalPath is the project's local folder on this machine, "" when the
	// project has none here.
	LocalPath string `json:"local_path"`
}

// IssueScope is a task's pairs as the server lists them.
type IssueScope struct {
	IssueID string `json:"issue_id"`
	Items   []Pair `json:"items"`
}

// TreeReport is what became of one tree.
type TreeReport struct {
	State      string `json:"state"`
	Path       string `json:"path,omitempty"`
	TaskBranch string `json:"task_branch,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ErrNotFound is returned for a request the server answered 404: a task it
// does not know, or — on Work — the task-tree routes it does not offer.
var ErrNotFound = errors.New("treeworker: not found on the server")

// ErrConflict is returned for a state report the server refused because the
// tree's state changed meanwhile.
var ErrConflict = errors.New("treeworker: the tree's state changed")

// API is the server's task-tree routes as the worker uses them. An
// implementation returns ErrNotFound and ErrConflict for a 404 and a 409.
type API interface {
	// Work lists the trees this machine has to build or remove.
	Work(ctx context.Context) ([]WorkItem, error)
	// Mature lists the trees of the tasks that have been complete for
	// longer than their workspace keeps them.
	Mature(ctx context.Context) ([]WorkItem, error)
	// Scope lists the pairs of the task issueID.
	Scope(ctx context.Context, issueID string) (*IssueScope, error)
	// ReportState records what became of the tree treeID of the task
	// issueID.
	ReportState(ctx context.Context, issueID, treeID string, report TreeReport) error
}

// Defaults of Config.
const (
	DefaultSteadyInterval = 30 * time.Second
	DefaultSlowInterval   = 5 * time.Minute
	DefaultInitialDelay   = 5 * time.Second
	DefaultParallelism    = 2
)

// Config is what a Worker needs to know. A zero time or parallelism takes its
// default.
type Config struct {
	// Root is the directory the task trees live under. It has to be absolute
	// and outside every git checkout, and the daemon owns it.
	Root string
	// SteadyInterval is the pause after a tick that found work or failures,
	// SlowInterval the pause after a quiet one. InitialDelay is how long
	// after Run starts the first tick follows.
	SteadyInterval time.Duration
	SlowInterval   time.Duration
	InitialDelay   time.Duration
	// Parallelism is how many tasks one tick works on at once.
	Parallelism int
}

// Worker reconciles the task trees under a root with what the server lists.
type Worker struct {
	api    API
	cfg    Config
	logger *slog.Logger
	// pause waits between ticks. Tests replace it to see the intervals.
	pause func(ctx context.Context, d time.Duration) error

	// unsupported and workFailed remember what the last work list call
	// ended in, so a change is logged once and not on every tick.
	unsupported bool
	workFailed  bool
}

// New makes the worker for api. The root is refused when it is not absolute,
// is a filesystem root, or lies inside a git checkout: tasktree would fail
// every task there, and trees inside a checkout show up in its status.
func New(api API, cfg Config, logger *slog.Logger) (*Worker, error) {
	if api == nil {
		return nil, errors.New("treeworker: the worker needs an API")
	}
	if logger == nil {
		logger = slog.Default()
	}
	root := filepath.Clean(strings.TrimSpace(cfg.Root))
	if _, err := tasktree.TaskDir(root, "task"); err != nil {
		return nil, err
	}
	if top, inside := enclosingCheckout(root); inside {
		return nil, fmt.Errorf("treeworker: task root %s is inside the git working tree %s; task trees belong outside every repository checkout", root, top)
	}
	cfg.Root = root
	if cfg.SteadyInterval <= 0 {
		cfg.SteadyInterval = DefaultSteadyInterval
	}
	if cfg.SlowInterval <= 0 {
		cfg.SlowInterval = DefaultSlowInterval
	}
	if cfg.InitialDelay <= 0 {
		cfg.InitialDelay = DefaultInitialDelay
	}
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = DefaultParallelism
	}
	return &Worker{api: api, cfg: cfg, logger: logger, pause: sleep}, nil
}

// Root is the directory the worker keeps the task trees under.
func (w *Worker) Root() string {
	return w.cfg.Root
}

// enclosingCheckout returns the git working tree that contains root, or the
// nearest existing directory above it, and whether there is one.
func enclosingCheckout(root string) (string, bool) {
	dir := filepath.Clean(root)
	for !pathExists(dir) {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	top, err := execenv.RunGitStdout(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(top), true
}

// Run ticks until ctx is done. A tick that found work or failures is followed
// by SteadyInterval, a quiet one by SlowInterval.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("task trees: worker started",
		"root", w.cfg.Root,
		"steady_interval", w.cfg.SteadyInterval,
		"slow_interval", w.cfg.SlowInterval,
		"parallelism", w.cfg.Parallelism,
	)
	if err := w.pause(ctx, w.cfg.InitialDelay); err != nil {
		return
	}
	for {
		busy := w.tick(ctx)
		if ctx.Err() != nil {
			return
		}
		pause := w.cfg.SlowInterval
		if busy {
			pause = w.cfg.SteadyInterval
		}
		if err := w.pause(ctx, pause); err != nil {
			return
		}
	}
}

// tick reconciles the disk with the server once: with the work list, then
// the mature list of the trees their workspaces no longer keep, which the
// server decides. It reports whether the next tick should follow soon.
func (w *Worker) tick(ctx context.Context) (busy bool) {
	work, err := w.api.Work(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		if errors.Is(err, ErrNotFound) {
			// The server keeps its task-tree routes behind a flag. While it
			// is off, one request per slow tick is all the worker costs.
			if !w.unsupported {
				w.unsupported = true
				w.logger.Info("task trees: the server does not offer task trees; checking again on every slow tick", "error", errText(err))
			}
			return false
		}
		w.logWorkFailure(err)
		return true
	}
	w.unsupported, w.workFailed = false, false
	busy = w.reconcile(ctx, work, false)
	if ctx.Err() != nil {
		return busy
	}
	mature, err := w.api.Mature(ctx)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrNotFound) {
			w.logger.Warn("task trees: could not read the mature list", "error", errText(err))
			busy = true
		}
	} else if w.reconcile(ctx, mature, true) {
		busy = true
	}
	return busy
}

// logWorkFailure reports a work list that could not be read once at Warn and
// the repeats of the same outage at Debug, so a server that is down for long
// does not fill the log.
func (w *Worker) logWorkFailure(err error) {
	if w.workFailed {
		w.logger.Debug("task trees: still could not read the work list", "error", errText(err))
		return
	}
	w.workFailed = true
	w.logger.Warn("task trees: could not read the work list", "error", errText(err))
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

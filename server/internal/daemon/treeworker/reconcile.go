package treeworker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/multica-ai/multica/server/internal/daemon/tasktree"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// maxErrorRunes bounds the error a state report carries, to what the server
// stores whole.
const maxErrorRunes = 500

// taskGroup is one task's items of a work or mature list.
type taskGroup struct {
	issueID string
	taskKey string
	creates []WorkItem
	removes []WorkItem
}

// reconcile works through the items task by task, Parallelism at a time, and
// reports whether a tree was created, removed or reported. One task's failure
// is its own: it is logged and does not touch the others.
func (w *Worker) reconcile(ctx context.Context, items []WorkItem, mature bool) bool {
	var acted atomic.Bool
	slots := make(chan struct{}, w.cfg.Parallelism)
	var wg sync.WaitGroup
	for _, group := range groupByTask(items) {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			// Work in flight cannot be cut short. It is let to finish rather
			// than left behind half done.
			wg.Wait()
			return acted.Load()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			if w.workTask(ctx, group, mature) {
				acted.Store(true)
			}
		}()
	}
	wg.Wait()
	return acted.Load()
}

// groupByTask sorts the items into one group per task, in the order the list
// names them. An item without a task, a tree or a known action is dropped.
func groupByTask(items []WorkItem) []taskGroup {
	var groups []taskGroup
	index := make(map[string]int, len(items))
	for _, item := range items {
		if item.IssueID == "" || item.TaskKey == "" || item.TreeID == "" {
			continue
		}
		i, ok := index[item.IssueID]
		if !ok {
			i = len(groups)
			index[item.IssueID] = i
			groups = append(groups, taskGroup{issueID: item.IssueID, taskKey: item.TaskKey})
		}
		switch item.Action {
		case ActionCreate:
			groups[i].creates = append(groups[i].creates, item)
		case ActionRemove:
			groups[i].removes = append(groups[i].removes, item)
		}
	}
	return groups
}

// workTask carries out one task's items and reports whether a tree was
// created, removed or reported. A mature list asks for removal whatever the
// trees' states; a work list only for a tree the task's pairs still show as
// they did when the list was made.
func (w *Worker) workTask(ctx context.Context, group taskGroup, mature bool) bool {
	taskDir, err := tasktree.TaskDir(w.cfg.Root, group.taskKey)
	if err != nil {
		w.logger.Warn("task trees: cannot place a task's directory",
			"issue_id", group.issueID, "task_key", group.taskKey, "error", errText(err))
		return false
	}
	if len(group.creates) == 0 && (len(group.removes) == 0 || !pathExists(taskDir)) {
		// Nothing to build, and nothing stands where the task's trees would:
		// there is no tree left to remove.
		return false
	}
	scope, err := w.api.Scope(ctx, group.issueID)
	switch {
	case err == nil:
	case errors.Is(err, ErrNotFound), ctx.Err() != nil:
		// The task is gone, or the daemon is stopping: nothing is left to do
		// for it.
		return false
	default:
		w.logger.Warn("task trees: could not read the pairs of a task",
			"issue_id", group.issueID, "error", errText(err))
		return true
	}

	acted := false
	if creates := pendingOnly(scope, group.creates); len(creates) > 0 {
		if _, _, err := w.ensure(ctx, group.taskKey, scope, treeIDs(creates)); err != nil {
			w.logger.Warn("task trees: could not build the trees of a task",
				"issue_id", group.issueID, "task_dir", taskDir, "error", errText(err))
		}
		acted = true
	}
	if removes := group.removes; len(removes) > 0 && pathExists(taskDir) {
		for _, item := range removes {
			if i, _ := scopeIndex(scope, item.TreeID); !mature && i >= 0 && scope.Items[i].Tree.State != StateDeleted {
				continue
			}
			if w.removeTree(ctx, taskDir, group.taskKey, scope, item) {
				acted = true
			}
		}
	}
	return acted
}

// EnsureTree makes sure the tree treeID of the task taskKey exists and
// returns its path. It is for code about to start a session in the tree, and
// goes through the same path as the tick — the state reports included — so
// the server learns what became of the tree. scope is the task's pair list.
// The error is the reason the tree could not be built.
func (w *Worker) EnsureTree(ctx context.Context, taskKey string, scope *IssueScope, treeID string) (string, error) {
	if scope == nil || treeID == "" {
		return "", errors.New("treeworker: a task's pair list and a tree are required")
	}
	if i, pair := scopeIndex(scope, treeID); i < 0 || pair.LocalPath == "" {
		return "", fmt.Errorf("treeworker: tree %s is not one of the pairs of task %s with a local folder on this machine", treeID, taskKey)
	}
	layout, pairs, err := w.ensure(ctx, taskKey, scope, []string{treeID})
	if err != nil {
		return "", err
	}
	return layout.Trees[pairIndex(pairs, treeID)].Path, nil
}

// ensure makes sure every tree of the task exists and reports each tree in
// treeIDs ready or failed. It is the one path that builds trees. It returns
// the layout and the pairs it was computed from, or the error the trees were
// reported failed with.
func (w *Worker) ensure(ctx context.Context, taskKey string, scope *IssueScope, treeIDs []string) (*tasktree.TaskLayout, []Pair, error) {
	pairs, specs := buildable(scope)
	var wanted, noFolder []string
	for _, id := range treeIDs {
		i, pair := scopeIndex(scope, id)
		switch {
		case i < 0:
			// Not one of the task's pairs, so there is no tree to report to.
		case pair.LocalPath == "":
			noFolder = append(noFolder, id)
		default:
			wanted = append(wanted, id)
		}
	}
	errNoFolder := errors.New("the project has no local folder on this machine")
	if len(noFolder) > 0 {
		w.reportFailed(ctx, scope.IssueID, noFolder, errNoFolder)
	}
	if len(wanted) == 0 {
		return nil, nil, errNoFolder
	}
	layout, err := tasktree.Create(w.cfg.Root, taskKey, specs, w.logger)
	if err != nil {
		w.reportFailed(ctx, scope.IssueID, wanted, err)
		return nil, nil, err
	}
	w.warnUnnamed(taskKey, layout)
	for _, id := range wanted {
		tree := layout.Trees[pairIndex(pairs, id)]
		w.report(ctx, scope.IssueID, id, TreeReport{State: StateReady, Path: tree.Path, TaskBranch: tree.Branch})
	}
	return layout, pairs, nil
}

// warnUnnamed logs trees standing in the task directory that the layout does
// not name, which is what a pair list that changed shape leaves behind. They
// are left alone: the work in them stays safe on their own task branches.
func (w *Worker) warnUnnamed(taskKey string, layout *tasktree.TaskLayout) {
	status, err := tasktree.Inspect(layout.Dir)
	if err != nil {
		return
	}
	named := make(map[string]bool, len(layout.Trees))
	for _, tree := range layout.Trees {
		named[tree.Path] = true
	}
	var stray []string
	for _, tree := range status.Trees {
		if !named[tree.Path] {
			stray = append(stray, tree.Path+" ("+tree.Branch+")")
		}
	}
	if len(stray) > 0 {
		w.logger.Warn("task trees: the task directory holds trees its pairs no longer name; they are left alone",
			"task_dir", layout.Dir, "trees", stray)
	}
}

// removeTree removes the tree item names and reports it deleted. It reports
// whether a tree was removed. A tree tasktree had to keep — work it could not
// finalize — stays, and so does its state on the server.
func (w *Worker) removeTree(ctx context.Context, taskDir, taskKey string, scope *IssueScope, item WorkItem) bool {
	if !tasktree.IsUnderRoot(w.cfg.Root, taskDir) {
		w.logger.Warn("task trees: a task directory lies outside the task root; leaving it alone", "path", taskDir)
		return false
	}
	name, whole := nameTree(w.cfg.Root, taskKey, scope, item)
	if name == "" {
		w.logger.Warn("task trees: cannot tell which tree of the task the server wants gone; leaving the task directory alone",
			"issue_id", item.IssueID, "task_dir", taskDir,
			"project_id", item.ProjectID, "base_branch", item.BaseBranch)
		return false
	}
	present, err := treePresent(taskDir, name)
	if err != nil {
		w.logger.Warn("task trees: could not inspect a task directory", "path", taskDir, "error", errText(err))
		return false
	}
	if !present && !whole {
		// Nothing of the tree is left to remove.
		w.logger.Debug("task trees: the tree to remove is already gone",
			"issue_id", item.IssueID, "tree_id", item.TreeID, "path", filepath.Join(taskDir, name))
		return false
	}

	var result *tasktree.DeleteResult
	if whole {
		// The tree is the task directory; removing it takes the directory too, and an empty one with it.
		result, err = tasktree.Delete(taskDir, w.logger)
	} else {
		result, err = tasktree.DeleteTree(taskDir, name, w.logger)
	}
	if !w.removalOutcome(result, err, taskDir) {
		return false
	}
	w.report(ctx, item.IssueID, item.TreeID, TreeReport{State: StateDeleted})
	return true
}

// nameTree says which tree in taskDir the item names: its name as
// tasktree.Inspect reports it, or — with whole set — the task directory
// itself, when the item's pair is the task's only one.
func nameTree(root, taskKey string, scope *IssueScope, item WorkItem) (name string, whole bool) {
	key := tasktree.TaskKey(taskKey)
	if scope != nil {
		pairs, specs := buildable(scope)
		if layout, err := tasktree.Layout(root, taskKey, specs); err == nil {
			if i := pairIndex(pairs, item.TreeID); i >= 0 {
				if len(layout.Trees) == 1 {
					return key, true
				}
				return layout.Trees[i].Slug, false
			}
		}
	}
	// The task's pairs no longer include the item's, so the layout cannot
	// name its tree. The branch it was last reported on still does.
	return nameFromBranch(item.TaskBranch, key)
}

// nameFromBranch reads the tree's name off a task branch.
func nameFromBranch(branch, key string) (name string, whole bool) {
	switch single, multi := tasktree.BranchPrefix+key, tasktree.BranchPrefix+key+"/"; {
	case branch == single:
		return key, true
	case strings.HasPrefix(branch, multi):
		return strings.TrimPrefix(branch, multi), false
	}
	return "", false
}

// treePresent reports whether a tree of the given name stands in taskDir.
func treePresent(taskDir, name string) (bool, error) {
	status, err := tasktree.Inspect(taskDir)
	if err != nil {
		return false, err
	}
	for _, tree := range status.Trees {
		if tree.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// removalOutcome judges what a removal did. A tree tasktree kept is logged
// and means nothing was removed, so that no state is reported for it.
func (w *Worker) removalOutcome(result *tasktree.DeleteResult, err error, path string) (removed bool) {
	if result != nil {
		for _, outcome := range result.Trees {
			if outcome.Kept {
				w.logger.Warn("task trees: a tree to remove was kept, because its work could not be finalized; it keeps its state",
					"path", outcome.Path, "branch", outcome.Branch, "error", errText(outcome.Err))
				return false
			}
		}
		removed = len(result.Trees) > 0
	}
	if err != nil {
		w.logger.Warn("task trees: a removal did not finish cleanly", "path", path, "error", errText(err))
	}
	return removed
}

// buildable returns the task's pairs that can have a tree here — those whose
// project has a local folder on this machine — in the server's order, with
// the spec of each. A pair without a folder takes no part in the layout: its
// tree could never be built, and the others must not wait for it.
func buildable(scope *IssueScope) (pairs []Pair, specs []tasktree.Spec) {
	pairs = make([]Pair, 0, len(scope.Items))
	for _, pair := range scope.Items {
		if pair.LocalPath != "" {
			pairs = append(pairs, pair)
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Position < pairs[j].Position })
	specs = make([]tasktree.Spec, len(pairs))
	for i, pair := range pairs {
		specs[i] = tasktree.Spec{Repo: pair.LocalPath, BaseBranch: pair.BaseBranch}
	}
	return pairs, specs
}

// scopeIndex returns the position of the pair whose tree is treeID in the
// order the server lists the task's pairs, and the pair; i is -1 when the
// task has no such pair.
func scopeIndex(scope *IssueScope, treeID string) (i int, pair Pair) {
	for i, pair = range scope.Items {
		if pair.Tree != nil && pair.Tree.ID == treeID {
			return i, pair
		}
	}
	return -1, Pair{}
}

// pairIndex returns the position of the pair whose tree is treeID, or -1.
func pairIndex(pairs []Pair, treeID string) int {
	for i, pair := range pairs {
		if pair.Tree != nil && pair.Tree.ID == treeID {
			return i
		}
	}
	return -1
}

// pendingOnly keeps the create items whose tree the task's pairs still show
// as pending. The work list is a moment old by the time the pairs arrive; a
// tree the server moved on from in between is left to its new state.
func pendingOnly(scope *IssueScope, items []WorkItem) []WorkItem {
	kept := items[:0:0]
	for _, item := range items {
		if i, _ := scopeIndex(scope, item.TreeID); i >= 0 && scope.Items[i].Tree.State == StatePending {
			kept = append(kept, item)
		}
	}
	return kept
}

func treeIDs(items []WorkItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.TreeID
	}
	return ids
}

// report records what became of a tree. A report that found the tree gone or
// its state changed needs no retry: the next tick goes by the server's state.
func (w *Worker) report(ctx context.Context, issueID, treeID string, report TreeReport) {
	if treeID == "" {
		return
	}
	err := w.api.ReportState(ctx, issueID, treeID, report)
	switch {
	case err == nil:
	case ctx.Err() != nil, errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict):
		w.logger.Debug("task trees: a state report was not recorded",
			"issue_id", issueID, "tree_id", treeID, "state", report.State, "error", errText(err))
	default:
		w.logger.Warn("task trees: could not report the state of a tree",
			"issue_id", issueID, "tree_id", treeID, "state", report.State, "error", errText(err))
	}
}

func (w *Worker) reportFailed(ctx context.Context, issueID string, treeIDs []string, cause error) {
	report := TreeReport{State: StateFailed, Error: shortError(cause)}
	for _, id := range treeIDs {
		w.report(ctx, issueID, id, report)
	}
}

// shortError reduces err to one redacted line a state report can carry.
func shortError(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "tasktree: "))
	line = redact.Text(line)
	if runes := []rune(line); len(runes) > maxErrorRunes {
		line = string(runes[:maxErrorRunes-1]) + "…"
	}
	return line
}

// errText is an error for a log line, redacted: git and the server quote
// URLs, and a URL may carry a credential.
func errText(err error) string {
	return redact.Text(err.Error())
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

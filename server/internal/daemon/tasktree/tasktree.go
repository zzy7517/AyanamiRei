// Package tasktree manages the git worktrees of a task.
//
// A task is a Multica issue. Under a configured root it owns one task
// directory, <root>/<task key>, holding one worktree ("tree") per project and
// branch pair: with a single pair the task directory is that worktree, with
// more each worktree is a subdirectory named by its slug. Every tree is cut
// from the user's own repository, and nothing in that checkout — files, index,
// stash, branches — is ever written to.
//
// Three properties this package exists to guarantee:
//
//  1. A tree starts from the latest commit of its base branch as that ref
//     stands locally. Nothing is fetched, and nothing uncommitted in the
//     user's checkout is carried over.
//  2. A task's work lives on its task branches. Rebuilding a deleted tree
//     continues its branch, and no branch is ever deleted here.
//  3. Nothing is silently discarded. Removing a tree first applies the
//     delivery rules of worktree mode (execenv.Finalize): what is left
//     uncommitted is committed to the task branch when the branch holds no
//     commit of its own, and saved under a hidden ref when it does. A tree
//     that cannot be finalized that way is kept and reported.
package tasktree

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// BranchPrefix namespaces every branch a task tree checks out.
const BranchPrefix = "task/"

// baseBranchFile names the file in a tree's private git dir that records the
// branch the tree was cut from. It lives with the worktree's registration, so
// git removes it with the tree and nothing has to track it.
const baseBranchFile = "multica-base-branch"

// Spec names one tree of a task: a repository and the branch it starts from.
type Spec struct {
	// Repo is an absolute path to the repository's working tree: its root or
	// any directory inside it.
	Repo string
	// BaseBranch is a local branch ("main") or a remote-tracking branch
	// ("origin/main") of Repo. Nothing is fetched: the tree starts from the
	// commit the ref holds locally.
	BaseBranch string
}

// Tree is one tree's place in a task.
type Tree struct {
	// Slug identifies the tree inside its task and, when the task has two or
	// more trees, names its subdirectory.
	Slug string
	// Path is the worktree's directory.
	Path string
	// Repo and BaseBranch are the spec the tree is laid out from.
	Repo       string
	BaseBranch string
	// Branch is the task branch checked out in the worktree.
	Branch string
}

// TaskLayout is where a task's trees live.
type TaskLayout struct {
	Root string
	// Key is the task key the layout was computed from.
	Key string
	// Dir is the task directory.
	Dir   string
	Trees []Tree
}

// TaskKey returns identifier as a task key: every character outside
// [A-Za-z0-9._-] becomes "-". The key is the task directory's name and part of
// every task branch.
func TaskKey(identifier string) string {
	return sanitize(identifier)
}

// TaskDir returns the task directory of taskKey under root.
func TaskDir(root, taskKey string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("tasktree: task root %q must be an absolute path", root)
	}
	root = filepath.Clean(root)
	if dir := filepath.Dir(root); dir == root {
		return "", fmt.Errorf("tasktree: task root %q is a filesystem root; task trees need a directory of their own", root)
	}
	key := TaskKey(taskKey)
	if !validComponent(key) {
		return "", fmt.Errorf("tasktree: %q is not a usable task key", taskKey)
	}
	return filepath.Join(root, key), nil
}

// Slugs returns each spec's slug in order: the base name of its repository
// path or, when the same repository path is in specs more than once, that name
// followed by "--" and the base branch with every "/" replaced by "-".
// Characters outside [A-Za-z0-9._-] become "-" as well, so a slug is always a
// usable directory name and branch component.
func Slugs(specs []Spec) ([]string, error) {
	repos := make([]string, len(specs))
	counts := make(map[string]int, len(specs))
	for i, spec := range specs {
		repo, err := specRepo(spec, i)
		if err != nil {
			return nil, err
		}
		repos[i] = repo
		counts[repo]++
	}
	slugs := make([]string, len(specs))
	for i, repo := range repos {
		slug := sanitize(filepath.Base(repo))
		if counts[repo] > 1 {
			slug += "--" + sanitize(specs[i].BaseBranch)
		}
		if !validComponent(slug) {
			return nil, fmt.Errorf("tasktree: tree %d: repository %q yields no usable slug %q", i+1, specs[i].Repo, slug)
		}
		slugs[i] = slug
	}
	return slugs, nil
}

// Layout returns where the trees of a task live: its directory and, for each
// spec in order, the worktree's path and task branch. It touches nothing on
// disk.
//
// With one spec the task directory is the worktree and the task branch is
// task/<task key>. With more, the task directory is a plain directory, each
// worktree is the subdirectory named by its slug, and the task branch is
// task/<task key>/<slug>.
func Layout(root, taskKey string, specs []Spec) (*TaskLayout, error) {
	dir, err := TaskDir(root, taskKey)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, errors.New("tasktree: a task needs at least one tree")
	}
	slugs, err := Slugs(specs)
	if err != nil {
		return nil, err
	}
	for i, spec := range specs {
		if !validBranchName(spec.BaseBranch) {
			return nil, fmt.Errorf("tasktree: tree %d: %q is not a usable branch name", i+1, spec.BaseBranch)
		}
	}

	layout := &TaskLayout{Root: filepath.Clean(root), Key: TaskKey(taskKey), Dir: dir}
	paths, branches := make(map[string]int, len(specs)), make(map[string]int, len(specs))
	for i, spec := range specs {
		tree := Tree{
			Slug:       slugs[i],
			Repo:       filepath.Clean(spec.Repo),
			BaseBranch: spec.BaseBranch,
			Branch:     BranchPrefix + layout.Key,
		}
		tree.Path = dir
		if len(specs) > 1 {
			tree.Path = filepath.Join(dir, tree.Slug)
			tree.Branch += "/" + tree.Slug
		}
		if !validBranchName(tree.Branch) {
			return nil, fmt.Errorf("tasktree: tree %d: %q yields no usable task branch %q", i+1, taskKey, tree.Branch)
		}
		if other, taken := paths[tree.Path]; taken {
			return nil, fmt.Errorf("tasktree: trees %d and %d would both be %s", other+1, i+1, tree.Path)
		}
		if other, taken := branches[tree.Branch]; taken {
			return nil, fmt.Errorf("tasktree: trees %d and %d would both be on branch %s", other+1, i+1, tree.Branch)
		}
		paths[tree.Path], branches[tree.Branch] = i, i
		layout.Trees = append(layout.Trees, tree)
	}
	return layout, nil
}

// specRepo validates spec's repository path and returns it cleaned.
func specRepo(spec Spec, i int) (string, error) {
	if spec.Repo == "" || !filepath.IsAbs(spec.Repo) {
		return "", fmt.Errorf("tasktree: tree %d: repository path %q must be an absolute path", i+1, spec.Repo)
	}
	return filepath.Clean(spec.Repo), nil
}

// sanitize keeps [A-Za-z0-9._-] and replaces every other character with "-".
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '-'
	}, s)
}

// validBranchName reports whether git would accept name as a branch name.
func validBranchName(name string) bool {
	if name == "" || name == "@" || strings.HasPrefix(name, "-") || strings.Contains(name, "@{") {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c == 0x7f || strings.IndexByte(" ~^:?*[\\", c) >= 0 {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if !validComponent(part) {
			return false
		}
	}
	return true
}

// validComponent reports whether s can be one component of a branch name — and
// so also a task directory's name.
func validComponent(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.HasSuffix(s, ".") &&
		!strings.HasSuffix(s, ".lock") && !strings.Contains(s, "..")
}

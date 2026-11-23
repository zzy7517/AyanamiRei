package tasktree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/util"
)

// gitOut runs git in dir and returns its stdout. The error names the command
// and carries git's stderr.
func gitOut(dir string, args ...string) (string, error) {
	out, err := execenv.RunGitStdout(dir, args...)
	if err != nil {
		return out, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func gitTrimmed(dir string, args ...string) (string, error) {
	out, err := gitOut(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// gitMessage reduces the error of a failed git invocation to git's own
// explanation on its first line.
func gitMessage(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	if i := strings.LastIndex(line, ": exit status"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// realish is path with every symlink that exists resolved and the rest kept
// as written, so a path that does not exist yet still compares equal to the
// path git records once it creates it.
func realish(path string) string {
	if resolved, err := util.ResolveSymlinksBestEffort(path); err == nil && resolved != "" {
		return resolved
	}
	return filepath.Clean(path)
}

// canonical is realish for a path that exists: the form git reports it by.
func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// isWorkTree reports whether path holds a worktree's .git entry.
func isWorkTree(path string) bool {
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}

// repository is a spec's repository as Create needs to see it.
type repository struct {
	// root is the resolved root of the working tree the spec named.
	root string
	// common is the resolved directory every worktree of the repository
	// shares: what two specs naming the same repository have in common.
	common string
}

func openRepository(path string) (*repository, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("tasktree: repository %s does not exist", path)
	case err != nil:
		return nil, fmt.Errorf("tasktree: repository %s: %w", path, err)
	case !info.IsDir():
		return nil, fmt.Errorf("tasktree: repository %s is not a directory", path)
	}
	root, err := gitTrimmed(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("tasktree: repository %s is not a git working tree: %s", path, gitMessage(err))
	}
	common, err := gitTrimmed(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("tasktree: repository %s: %w", path, err)
	}
	// git answers relative to the working tree it ran in when that is the
	// main one, and absolute from a linked worktree.
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	if _, err := gitOut(root, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return nil, fmt.Errorf("tasktree: repository %s has no commit to start a tree from; make an initial commit first", path)
	}
	return &repository{root: canonical(root), common: canonical(common)}, nil
}

// baseRef returns the full name of the local or remote-tracking branch called
// base in the working tree at path, or "" when the repository has neither.
func baseRef(path, base string) string {
	candidates := []string{"refs/heads/" + base, "refs/remotes/" + base}
	if strings.HasPrefix(base, "refs/") {
		candidates = append(candidates, base)
	}
	for _, ref := range candidates {
		if _, err := gitOut(path, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref
		}
	}
	return ""
}

// worktreeEntry is one worktree a repository has registered.
type worktreeEntry struct {
	path   string
	branch string // "refs/heads/<name>", or "" when HEAD is detached
	main   bool
}

func listWorktrees(repoPath string) ([]worktreeEntry, error) {
	out, err := gitOut(repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []worktreeEntry
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			entries = append(entries, worktreeEntry{path: strings.TrimPrefix(line, "worktree "), main: len(entries) == 0})
		case strings.HasPrefix(line, "branch "):
			if n := len(entries); n > 0 {
				entries[n-1].branch = strings.TrimPrefix(line, "branch ")
			}
		}
	}
	return entries, nil
}

// treeInfo is what git says about the working tree at one path.
type treeInfo struct {
	gitDir string // the working tree's private git dir
	common string
	top    string // the working tree's root as git reports it
	branch string // the checked-out branch, or "" when HEAD is detached
	// detached is set when HEAD does not point at a branch.
	detached bool
	// problem says why path is not a working tree at all; "" when it is one.
	problem string
}

func probe(path string) treeInfo {
	out, err := gitOut(path, "rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		return treeInfo{problem: "not a git working tree (" + gitMessage(err) + ")"}
	}
	top, rest, ok := strings.Cut(out, "\n")
	gitDir, common, _ := strings.Cut(rest, "\n")
	gitDir, common = strings.TrimSpace(gitDir), strings.TrimSpace(common)
	if !ok || gitDir == "" || common == "" {
		return treeInfo{problem: "git did not describe the working tree"}
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(top, common)
	}
	info := treeInfo{
		gitDir: filepath.FromSlash(canonical(gitDir)),
		common: canonical(common),
		top:    canonical(top),
	}
	if ref, err := gitOut(path, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		info.branch = strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	} else {
		info.detached = true
	}
	return info
}

// mainWorkTree returns the repository's main working tree, or "" when git
// cannot list it.
func mainWorkTree(path string) string {
	entries, err := listWorktrees(path)
	if err != nil || len(entries) == 0 {
		return ""
	}
	return entries[0].path
}

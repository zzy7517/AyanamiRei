package terminal

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestResolveCwd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only the Linux host can enter directories (canEnter)")
	}
	base := t.TempDir()
	tasks := filepath.Join(base, "tasks")
	projects := filepath.Join(base, "projects")
	roots := []string{tasks, projects}
	taskTree := filepath.Join(tasks, "ws", "task")
	project := filepath.Join(projects, "app")
	outside := filepath.Join(base, "outside")
	sibling := filepath.Join(base, "tasks-other")
	file := filepath.Join(tasks, "notes.txt")
	alias := filepath.Join(base, "projects-alias")
	// A root the daemon owns but has not written to yet.
	missing := filepath.Join(base, "missing")
	for _, dir := range []string{taskTree, project, outside, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		filepath.Join(tasks, "link-inside"):     taskTree,
		filepath.Join(tasks, "link-outside"):    outside,
		filepath.Join(projects, "link-to-task"): taskTree,
		alias:                                   projects,
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	// The answer is a fully resolved path, whichever way the test directory
	// is reached on this machine.
	resolved := func(p string) string {
		out, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	for _, tc := range []struct {
		name  string
		roots []string
		cwd   string
		want  string
		code  string
	}{
		{"a task tree", roots, taskTree, taskTree, ""},
		{"the task root itself", roots, tasks, tasks, ""},
		{"a folder under the default project folder", roots, project, project, ""},
		{"the default project folder itself", roots, projects, projects, ""},
		{"a task tree reached through the project folder's symlink", roots, filepath.Join(projects, "link-to-task"), taskTree, ""},
		{"a root given through a symlink", []string{alias}, filepath.Join(alias, "app"), project, ""},
		{"inside through a symlink", roots, filepath.Join(tasks, "link-inside"), taskTree, ""},
		{"outside every root", roots, outside, "", protocol.TerminalErrPathNotAllowed},
		{"a sibling whose name has a root as a prefix", roots, sibling, "", protocol.TerminalErrPathNotAllowed},
		{"outside through a symlink inside a root", roots, filepath.Join(tasks, "link-outside"), "", protocol.TerminalErrPathNotAllowed},
		{"a dot-dot path out of the roots", roots, filepath.Join(tasks, "..", "outside"), "", protocol.TerminalErrPathNotAllowed},
		{"inside the root that is not listed", []string{tasks}, project, "", protocol.TerminalErrPathNotAllowed},
		{"beside a root that is not there yet", []string{missing, projects}, project, project, ""},
		{"beside a root that is a file", []string{file, projects}, project, project, ""},
		{"roots that allow nothing", []string{missing, file}, outside, "", protocol.TerminalErrPathNotAllowed},
		{"no root at all", nil, outside, "", protocol.TerminalErrPathNotAllowed},
		{"a missing directory", roots, filepath.Join(tasks, "missing"), "", protocol.TerminalErrBadRequest},
		{"a file", roots, file, "", protocol.TerminalErrBadRequest},
		{"a relative path", roots, "projects/app", "", protocol.TerminalErrBadRequest},
		{"empty", roots, "", "", protocol.TerminalErrBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, refusal := ResolveCwd(tc.roots, tc.cwd)
			if tc.code == "" {
				if refusal != nil {
					t.Fatalf("refused with %v, want %s", refusal, tc.want)
				}
				if want := resolved(tc.want); got != want {
					t.Fatalf("resolved %q, want %q", got, want)
				}
				return
			}
			if refusal == nil || refusal.Code != tc.code {
				t.Fatalf("refusal = %v, want %s", refusal, tc.code)
			}
			if got != "" {
				t.Fatalf("a refused request resolved to %q", got)
			}
		})
	}
}

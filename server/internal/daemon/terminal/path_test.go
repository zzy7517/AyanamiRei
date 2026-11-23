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
	root := filepath.Join(base, "root")
	inside := filepath.Join(root, "projects", "app")
	outside := filepath.Join(base, "outside")
	sibling := filepath.Join(base, "root-other")
	file := filepath.Join(root, "notes.txt")
	alias := filepath.Join(base, "root-alias")
	for _, dir := range []string{inside, outside, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		filepath.Join(root, "link-inside"):  inside,
		filepath.Join(root, "link-outside"): outside,
		alias:                               root,
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
		name, root, cwd string
		want            string
		code            string
	}{
		{"the root itself", root, root, root, ""},
		{"a directory inside", root, inside, inside, ""},
		{"inside the root given through a symlink", alias, filepath.Join(alias, "projects"), filepath.Join(root, "projects"), ""},
		{"inside through a symlink", root, filepath.Join(root, "link-inside"), inside, ""},
		{"outside the root", root, outside, "", protocol.TerminalErrPathNotAllowed},
		{"a sibling whose name has the root as a prefix", root, sibling, "", protocol.TerminalErrPathNotAllowed},
		{"outside through a symlink inside the root", root, filepath.Join(root, "link-outside"), "", protocol.TerminalErrPathNotAllowed},
		{"a dot-dot path out of the root", root, filepath.Join(root, "..", "outside"), "", protocol.TerminalErrPathNotAllowed},
		{"a missing directory", root, filepath.Join(root, "missing"), "", protocol.TerminalErrBadRequest},
		{"a file", root, file, "", protocol.TerminalErrBadRequest},
		{"a relative path", root, "projects/app", "", protocol.TerminalErrBadRequest},
		{"empty", root, "", "", protocol.TerminalErrBadRequest},
		{"an unset root", "", inside, "", protocol.TerminalErrTerminalRootUnconfigured},
		{"a relative root", "root", inside, "", protocol.TerminalErrTerminalRootUnconfigured},
		{"a missing root", filepath.Join(base, "no-root"), inside, "", protocol.TerminalErrTerminalRootUnconfigured},
		{"a root that is a file", file, file, "", protocol.TerminalErrTerminalRootUnconfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, refusal := ResolveCwd(tc.root, tc.cwd)
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

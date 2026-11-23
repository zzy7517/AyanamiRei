package tasktree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestMain keeps the repositories the tests make — "the user's" — clear of the
// machine's git configuration. A global hook, signing setup or trace target
// would act on them without the library having anything to do with it.
func TestMain(m *testing.M) {
	config, err := os.CreateTemp("", "tasktree-gitconfig-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create an empty global git config:", err)
		os.Exit(1)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", config.Name())
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	code := m.Run()
	config.Close()
	os.Remove(config.Name())
	os.Exit(code)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// requireGit skips the test when git is missing. Once past it, a failing git
// command is part of the assertion surface and fails the test.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not available: %v", err)
	}
}

func gitTry(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s failed: %s: %v", args, dir, out, err)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// newRepo creates a repository with one commit on main. It carries its own
// identity, and no automatic maintenance, so tests depend on neither.
func newRepo(t *testing.T, name string) string {
	t.Helper()
	requireGit(t)
	base := t.TempDir()
	// macOS hands out temp dirs that are symlinks; compare against the
	// canonical form everywhere.
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	repo := filepath.Join(base, name)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	gitRun(t, repo, "init", "-q", "-b", "main")
	for _, kv := range [][2]string{
		{"user.name", "Test User"}, {"user.email", "test@test.com"},
		{"gc.auto", "0"}, {"maintenance.auto", "false"},
	} {
		gitRun(t, repo, "config", kv[0], kv[1])
	}
	commitFile(t, repo, "README", "readme\n", "initial")
	return repo
}

// commitFile writes a file and commits it, returning the commit.
func commitFile(t *testing.T, repo, name, content, message string) string {
	t.Helper()
	writeFile(t, filepath.Join(repo, name), content)
	gitRun(t, repo, "add", "--", name)
	gitRun(t, repo, "commit", "-q", "-m", message)
	return gitRun(t, repo, "rev-parse", "HEAD")
}

// cloneRepo clones origin into a working tree of its own and gives it a
// committer identity.
func cloneRepo(t *testing.T, origin, name string) string {
	t.Helper()
	repo := filepath.Join(filepath.Dir(origin), name)
	if out, err := gitTry(filepath.Dir(origin), "clone", "-q", origin, name); err != nil {
		t.Fatalf("clone %s: %s: %v", origin, out, err)
	}
	gitRun(t, repo, "config", "user.name", "Test User")
	gitRun(t, repo, "config", "user.email", "test@test.com")
	gitRun(t, repo, "config", "gc.auto", "0")
	gitRun(t, repo, "config", "maintenance.auto", "false")
	return repo
}

func taskDirOf(t *testing.T, root, key string) string {
	t.Helper()
	dir, err := TaskDir(root, key)
	if err != nil {
		t.Fatalf("TaskDir: %v", err)
	}
	return dir
}

// registeredWorktrees lists the paths a repository has registered.
func registeredWorktrees(t *testing.T, repo string) []string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(gitRun(t, repo, "worktree", "list", "--porcelain"), "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, path)
		}
	}
	return paths
}

func wantError(t *testing.T, err error, substrings ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want an error containing %q, got nil", substrings)
	}
	for _, want := range substrings {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestTaskKey(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"MUL-123":   "MUL-123",
		"a.b_c-d":   "a.b_c-d",
		"MUL 7":     "MUL-7",
		"feature/x": "feature-x",
		"":          "",
		"issue#9":   "issue-9",
		"issue/中文":  "issue---",
		"./x":       ".-x",
	} {
		if got := TaskKey(in); got != want {
			t.Errorf("TaskKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		specs []Spec
		want  []string
	}{
		{"one tree", []Spec{{"/repos/app", "main"}}, []string{"app"}},
		{"trailing slash", []Spec{{"/repos/app/", "main"}}, []string{"app"}},
		{"two repositories", []Spec{{"/repos/app", "main"}, {"/repos/web", "dev"}}, []string{"app", "web"}},
		{
			"same repository twice",
			[]Spec{{"/repos/app", "main"}, {"/repos/app", "dev"}},
			[]string{"app--main", "app--dev"},
		},
		{
			"slash in the base branch",
			[]Spec{{"/repos/app", "main"}, {"/repos/app", "release/1.0"}},
			[]string{"app--main", "app--release-1.0"},
		},
		{
			"same repository spelled two ways",
			[]Spec{{"/repos/app", "main"}, {"/repos/app/.", "dev"}},
			[]string{"app--main", "app--dev"},
		},
		{
			"only a repeated repository carries its branch",
			[]Spec{{"/repos/app", "main"}, {"/repos/app", "dev"}, {"/repos/web", "main"}},
			[]string{"app--main", "app--dev", "web"},
		},
		{"characters outside the allowed set", []Spec{{"/repos/my app", "x"}}, []string{"my-app"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Slugs(tt.specs)
			if err != nil {
				t.Fatalf("Slugs: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Slugs = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlugsRejectsUnusableNames(t *testing.T) {
	t.Parallel()
	for name, specs := range map[string][]Spec{
		"empty repository path": {{"", "main"}},
		"relative repository":   {{"repos/app", "main"}},
		"hidden repository":     {{"/repos/.app", "main"}},
		"dot-dot in the name":   {{"/repos/a..b", "main"}},
		"trailing dot":          {{"/repos/app.", "main"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Slugs(specs); err == nil {
				t.Fatalf("Slugs(%v) succeeded, want an error", specs)
			}
		})
	}
}

func TestLayout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		root  string
		key   string
		specs []Spec
		want  *TaskLayout
	}{
		{
			name:  "one tree: the task directory is the worktree",
			root:  "/tasks",
			key:   "MUL-1",
			specs: []Spec{{"/repos/app", "main"}},
			want: &TaskLayout{Root: "/tasks", Key: "MUL-1", Dir: "/tasks/MUL-1", Trees: []Tree{
				{Slug: "app", Path: "/tasks/MUL-1", Repo: "/repos/app", BaseBranch: "main", Branch: "task/MUL-1"},
			}},
		},
		{
			name:  "two trees: one subdirectory each",
			root:  "/tasks/",
			key:   "MUL-1",
			specs: []Spec{{"/repos/app", "main"}, {"/repos/web", "dev"}},
			want: &TaskLayout{Root: "/tasks", Key: "MUL-1", Dir: "/tasks/MUL-1", Trees: []Tree{
				{Slug: "app", Path: "/tasks/MUL-1/app", Repo: "/repos/app", BaseBranch: "main", Branch: "task/MUL-1/app"},
				{Slug: "web", Path: "/tasks/MUL-1/web", Repo: "/repos/web", BaseBranch: "dev", Branch: "task/MUL-1/web"},
			}},
		},
		{
			name:  "the same repository twice on different branches",
			root:  "/tasks",
			key:   "MUL-1",
			specs: []Spec{{"/repos/app", "main"}, {"/repos/app", "feature/login"}},
			want: &TaskLayout{Root: "/tasks", Key: "MUL-1", Dir: "/tasks/MUL-1", Trees: []Tree{
				{Slug: "app--main", Path: "/tasks/MUL-1/app--main", Repo: "/repos/app", BaseBranch: "main", Branch: "task/MUL-1/app--main"},
				{Slug: "app--feature-login", Path: "/tasks/MUL-1/app--feature-login", Repo: "/repos/app", BaseBranch: "feature/login", Branch: "task/MUL-1/app--feature-login"},
			}},
		},
		{
			name:  "the task key is sanitized",
			root:  "/tasks",
			key:   "mul 7",
			specs: []Spec{{"/repos/app", "main"}},
			want: &TaskLayout{Root: "/tasks", Key: "mul-7", Dir: "/tasks/mul-7", Trees: []Tree{
				{Slug: "app", Path: "/tasks/mul-7", Repo: "/repos/app", BaseBranch: "main", Branch: "task/mul-7"},
			}},
		},
		{
			name:  "a remote-tracking base branch",
			root:  "/tasks",
			key:   "MUL-1",
			specs: []Spec{{"/repos/app", "origin/main"}},
			want: &TaskLayout{Root: "/tasks", Key: "MUL-1", Dir: "/tasks/MUL-1", Trees: []Tree{
				{Slug: "app", Path: "/tasks/MUL-1", Repo: "/repos/app", BaseBranch: "origin/main", Branch: "task/MUL-1"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout, err := Layout(tt.root, tt.key, tt.specs)
			if err != nil {
				t.Fatalf("Layout: %v", err)
			}
			if !reflect.DeepEqual(layout, tt.want) {
				t.Fatalf("Layout = %+v\nwant %+v", layout, tt.want)
			}
		})
	}
}

func TestLayoutRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	repo := Spec{"/repos/app", "main"}
	for name, tc := range map[string]struct {
		root     string
		key      string
		specs    []Spec
		mentions string
	}{
		"no trees":              {"/tasks", "MUL-1", nil, "at least one tree"},
		"empty root":            {"", "MUL-1", []Spec{repo}, "absolute"},
		"relative root":         {"tasks", "MUL-1", []Spec{repo}, "absolute"},
		"filesystem root":       {"/", "MUL-1", []Spec{repo}, "filesystem root"},
		"relative repository":   {"/tasks", "MUL-1", []Spec{{"repos/app", "main"}}, "absolute"},
		"empty task key":        {"/tasks", "", []Spec{repo}, "task key"},
		"task key .":            {"/tasks", ".", []Spec{repo}, "task key"},
		"task key ..":           {"/tasks", "..", []Spec{repo}, "task key"},
		"task key .hidden":      {"/tasks", ".hidden", []Spec{repo}, "task key"},
		"task key with dot-dot": {"/tasks", "a..b", []Spec{repo}, "task key"},
		"empty base branch":     {"/tasks", "MUL-1", []Spec{{"/repos/app", ""}}, "branch name"},
		"option-like branch":    {"/tasks", "MUL-1", []Spec{{"/repos/app", "-x"}}, "branch name"},
		"hidden branch":         {"/tasks", "MUL-1", []Spec{{"/repos/app", ".main"}}, "branch name"},
		"locked branch":         {"/tasks", "MUL-1", []Spec{{"/repos/app", "v1.lock"}}, "branch name"},
		"branch with dot-dot":   {"/tasks", "MUL-1", []Spec{{"/repos/app", "a..b"}}, "branch name"},
		"branch with a space":   {"/tasks", "MUL-1", []Spec{{"/repos/app", "my branch"}}, "branch name"},
		"trailing slash":        {"/tasks", "MUL-1", []Spec{{"/repos/app", "main/"}}, "branch name"},
		"same repository and base branch twice": {
			"/tasks", "MUL-1", []Spec{repo, repo}, "trees 1 and 2",
		},
		"same repository and base branch by another path": {
			"/tasks", "MUL-1", []Spec{repo, {"/repos/app/", "main"}}, "trees 1 and 2",
		},
		"two repositories whose slugs collide": {
			"/tasks", "MUL-1", []Spec{{"/repos/my app", "main"}, {"/repos/my-app", "dev"}}, "trees 1 and 2",
		},
	} {
		t.Run(name, func(t *testing.T) {
			layout, err := Layout(tc.root, tc.key, tc.specs)
			if layout != nil {
				t.Fatalf("Layout returned %+v, want nil", layout)
			}
			wantError(t, err, "tasktree:", tc.mentions)
		})
	}
}

func TestIsUnderRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{filepath.Join(root, "a", "b"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// root/link leads out of the root; alias leads into it.
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		root, path string
		want       bool
	}{
		"below the root":        {root, filepath.Join(root, "a", "b"), true},
		"the root itself":       {root, root, true},
		"a sibling":             {root, outside, false},
		"escaping with dot-dot": {root, filepath.Join(root, "a", "..", "..", "outside"), false},
		"through a link out":    {root, filepath.Join(root, "link"), false},
		"below a link out":      {root, filepath.Join(root, "link", "secret"), false},
		"missing path":          {root, filepath.Join(root, "missing"), false},
		"missing root":          {filepath.Join(base, "missing"), outside, false},
		"root spelled by alias": {alias, filepath.Join(root, "a", "b"), true},
		"path spelled by alias": {root, filepath.Join(alias, "a", "b"), true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IsUnderRoot(tc.root, tc.path); got != tc.want {
				t.Fatalf("IsUnderRoot(%s, %s) = %v, want %v", tc.root, tc.path, got, tc.want)
			}
		})
	}
}

func TestLockTaskIsPerTaskDirectory(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	task := filepath.Join(base, "MUL-1")
	if err := os.MkdirAll(task, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(task, alias); err != nil {
		t.Fatal(err)
	}

	// lockName takes the lock of one task directory and reports when it has it.
	lockName := func(dir string) <-chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			lockTask(dir)()
		}()
		return done
	}
	unlock := lockTask(task)
	for _, spelling := range []string{task + string(filepath.Separator), alias, alias + string(filepath.Separator)} {
		select {
		case <-lockName(spelling):
			t.Fatalf("lockTask(%q) was taken while %s is locked", spelling, task)
		case <-time.After(100 * time.Millisecond):
		}
	}
	select {
	case <-lockName(filepath.Join(base, "MUL-2")):
	case <-time.After(5 * time.Second):
		t.Fatal("the lock of another task directory blocked")
	}
	unlock()
	// With the lock released every spelling can take it in turn.
	for _, spelling := range []string{task, alias} {
		select {
		case <-lockName(spelling):
		case <-time.After(5 * time.Second):
			t.Fatalf("lockTask(%q) blocked after the lock was released", spelling)
		}
	}
}

// userState is everything of a user's checkout that the operations on task
// trees promise to leave alone.
type userState struct {
	status string
	head   string
	ref    string
	index  string
	stash  string
	reflog string
	config string
	refs   string
	files  string
}

// hashTree digests every file of the working tree, outside .git.
func hashTree(t *testing.T, repo string) string {
	t.Helper()
	var files strings.Builder
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.IsDir():
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		case !entry.Type().IsRegular():
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files.WriteString(rel + "\x00" + string(b) + "\x00")
		return nil
	})
	if err != nil {
		t.Fatalf("hash the working tree of %s: %v", repo, err)
	}
	sum := sha256.Sum256([]byte(files.String()))
	return hex.EncodeToString(sum[:])
}

func snapshotUser(t *testing.T, repo string) userState {
	t.Helper()
	// --no-optional-locks keeps the snapshot's own status from rewriting the
	// index it is about to hash.
	quiet := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "--no-optional-locks"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s failed: %s: %v", args, repo, out, err)
		}
		return string(out)
	}
	index, err := exec.Command("git", "-C", repo, "rev-parse", "--git-path", "index").Output()
	if err != nil {
		t.Fatalf("locate the index of %s: %v", repo, err)
	}
	// git answers relative to the working tree it ran in when that is the
	// main one.
	indexPath := strings.TrimSpace(string(index))
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(repo, indexPath)
	}
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read the index of %s: %v", repo, err)
	}
	sum := sha256.Sum256(indexBytes)
	// Task branches and the hidden refs of their trees are what the operations
	// add; no other ref may move.
	var refs []string
	for _, ref := range strings.Split(quiet("for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if !strings.HasPrefix(ref, "refs/heads/task/") && !strings.HasPrefix(ref, "refs/multica/") {
			refs = append(refs, ref)
		}
	}
	return userState{
		status: quiet("status", "--porcelain=v1", "-uall"),
		head:   quiet("rev-parse", "HEAD"),
		ref:    quiet("symbolic-ref", "HEAD"),
		index:  hex.EncodeToString(sum[:]),
		stash:  quiet("stash", "list", "--format=%gd %H"),
		reflog: quiet("reflog", "show", "HEAD", "--format=%H"),
		config: quiet("config", "--local", "--list"),
		refs:   strings.Join(refs, "\n"),
		files:  hashTree(t, repo),
	}
}

func assertUserUntouched(t *testing.T, before, after userState) {
	t.Helper()
	if before == after {
		return
	}
	vb, va := reflect.ValueOf(before), reflect.ValueOf(after)
	var diff []string
	for i := 0; i < vb.NumField(); i++ {
		b, a := vb.Field(i).String(), va.Field(i).String()
		if b != a {
			diff = append(diff, fmt.Sprintf("%s:\n  before %q\n  after  %q", vb.Type().Field(i).Name, b, a))
		}
	}
	sort.Strings(diff)
	t.Fatalf("the user's checkout changed:\n%s", strings.Join(diff, "\n"))
}

// waitForFile blocks until path shows up.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !exists(path); {
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// installHook gives the repository an executable git hook.
func installHook(t *testing.T, repo, name, script string) {
	t.Helper()
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "config", "core.hooksPath", hooks)
}

// breakCommitting makes every commit in the repository fail the way a signing
// setup without a usable key would: --no-verify does not disable signing. The
// format is pinned so that a host signing with ssh or x509 cannot sign.
func breakCommitting(t *testing.T, repo string) {
	t.Helper()
	gitRun(t, repo, "config", "commit.gpgSign", "true")
	gitRun(t, repo, "config", "gpg.format", "openpgp")
	gitRun(t, repo, "config", "gpg.program", filepath.Join(t.TempDir(), "not-a-gpg"))
}

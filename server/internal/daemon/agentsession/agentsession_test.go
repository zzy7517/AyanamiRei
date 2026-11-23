package agentsession

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/agenthooks"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// newLauncher starts a hook receiver and points Claude Code's store at an
// empty directory, so nothing on this machine is read or run: the executable
// below is a path that does not exist.
func newLauncher(t *testing.T, skillSettings string) (*Launcher, *agenthooks.Manager) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hooks, err := agenthooks.New(agenthooks.Options{Logger: logger})
	if err != nil {
		t.Fatalf("hook receiver: %v", err)
	}
	t.Cleanup(func() { _ = hooks.Close() })
	return NewLauncher(Config{Claude: "/opt/nowhere/claude", Hooks: hooks, SkillSettings: []byte(skillSettings), Logger: logger}), hooks
}

func settingsPath(t *testing.T, argv []string) string {
	t.Helper()
	i := slices.Index(argv, "--settings")
	if i < 0 || i+1 >= len(argv) {
		t.Fatalf("argv has no --settings <file>: %q", argv)
	}
	return argv[i+1]
}

func postHook(t *testing.T, hooks *agenthooks.Manager, event, session, cwd string) {
	t.Helper()
	body := fmt.Sprintf(`{"hook_event_name":%q,"session_id":%q,"cwd":%q}`, event, session, cwd)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/hook", hooks.Port()), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mintToken(t, hooks))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s hook: %v", event, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("post %s hook: status %d", event, resp.StatusCode)
	}
}

func mintToken(t *testing.T, hooks *agenthooks.Manager) string {
	t.Helper()
	token, err := hooks.MintToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return token
}

func nextTurnEnd(t *testing.T, hooks *agenthooks.Manager) agenthooks.TurnEnd {
	t.Helper()
	select {
	case end := <-hooks.TurnEnds():
		return end
	case <-time.After(5 * time.Second):
		t.Fatal("no turn end was reported")
		return agenthooks.TurnEnd{}
	}
}

func TestLaunchBuildsTheCommand(t *testing.T) {
	l, hooks := newLauncher(t, `{"permissions":{"deny":["Skill(hidden)"]}}`)
	tree := t.TempDir()
	other := t.TempDir()
	gone := filepath.Join(t.TempDir(), "removed")

	cmd, refusal := l.Launch(tree, &protocol.DaemonTerminalAgent{
		IssueID: "issue-1", StartTreeID: "tree-1", OtherDirs: []string{other, gone}, Brief: "MUL-1 do it",
	})
	if refusal != nil {
		t.Fatalf("launch refused: %v", refusal)
	}
	path := settingsPath(t, cmd.Argv)

	// No conversation is stored for the tree yet, so none is continued; a
	// tree that is gone from disk is not added.
	want := []string{
		"/opt/nowhere/claude", "--permission-mode", "bypassPermissions",
		"--add-dir", other,
		"--append-system-prompt", "MUL-1 do it", "--settings", path,
	}
	if !slices.Equal(cmd.Argv, want) {
		t.Fatalf("argv = %q, want %q", cmd.Argv, want)
	}
	if !slices.Contains(cmd.Env, "TERM=xterm-256color") || !slices.Contains(cmd.Env, "HOME="+os.Getenv("HOME")) {
		t.Fatalf("env has neither the daemon's variables nor the terminal type: %q", cmd.Env)
	}

	// The settings file is private to this daemon's user and carries the
	// skill policy, the hooks, and no bypass confirmation.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %v, want -rw-------", info.Mode().Perm())
	}
	if dir, err := os.Stat(filepath.Dir(path)); err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("settings directory = %v, %v; want mode drwx------", dir, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings are not JSON: %v", err)
	}
	if string(settings["skipDangerousModePermissionPrompt"]) != "true" {
		t.Fatalf("settings lack skipDangerousModePermissionPrompt: %s", data)
	}
	if !strings.Contains(string(settings["permissions"]), `"Skill(hidden)"`) {
		t.Fatalf("settings lost the skill policy: %s", data)
	}
	var hookSettings map[string][]struct {
		Hooks []struct{ Command string }
	}
	if err := json.Unmarshal(settings["hooks"], &hookSettings); err != nil {
		t.Fatalf("hooks are not JSON: %v", err)
	}
	command := hookSettings["Stop"]
	if len(command) != 1 || len(command[0].Hooks) != 1 || !strings.Contains(command[0].Hooks[0].Command, "127.0.0.1:"+strconv.Itoa(hooks.Port())) {
		t.Fatalf("Stop hook does not report to the receiver on port %d: %s", hooks.Port(), settings["hooks"])
	}

	// A turn that ends in the tree reports the issue of the terminal that
	// started there; a prompt alone does not.
	postHook(t, hooks, "UserPromptSubmit", "s1", tree)
	select {
	case end := <-hooks.TurnEnds():
		t.Fatalf("a prompt was reported as a turn end: %+v", end)
	case <-time.After(100 * time.Millisecond):
	}
	postHook(t, hooks, "Stop", "s1", tree)
	if end := nextTurnEnd(t, hooks); end != (agenthooks.TurnEnd{IssueID: "issue-1", Cwd: tree, SessionID: "s1"}) {
		t.Fatalf("turn end = %+v", end)
	}

	// Once the terminal is gone so are the file and the mapping.
	cmd.Cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("the settings directory outlived the terminal: %v", err)
	}
	postHook(t, hooks, "Stop", "s2", tree)
	if end := nextTurnEnd(t, hooks); end.IssueID != "" {
		t.Fatalf("turn end after the cleanup still names %q", end.IssueID)
	}
}

func TestLaunchContinuesWhatClaudeCodeAlreadyKeeps(t *testing.T) {
	// A tree's path keeps the dots and underscores of a repository name.
	tree := "/home/ada/tasks/MUL-7/my_repo.v2"
	for bucket, want := range map[string]bool{
		// Claude Code writes one dash for every character outside letters and digits.
		"-home-ada-tasks-MUL-7-my-repo-v2": true,
		"-home-ada-tasks-MUL-7-my_repo.v2": false,
	} {
		t.Run(bucket, func(t *testing.T) {
			l, _ := newLauncher(t, "")
			if err := os.MkdirAll(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", bucket), 0o755); err != nil {
				t.Fatal(err)
			}

			cmd, refusal := l.Launch(tree, &protocol.DaemonTerminalAgent{IssueID: "issue-1", Brief: "brief"})
			if refusal != nil {
				t.Fatalf("launch refused: %v", refusal)
			}
			defer cmd.Cleanup()
			if got := slices.Contains(cmd.Argv, "--continue"); got != want {
				t.Fatalf("--continue in argv = %v, want %v: %q", got, want, cmd.Argv)
			}
		})
	}
}

func TestLaunchContinuesADirectoryWhoseNameClaudeCodeCuts(t *testing.T) {
	l, _ := newLauncher(t, "")
	// Claude Code stores a name longer than 200 characters as its first 200
	// and a hash of its own.
	name := strings.TrimRight("-home-ada-"+strings.Repeat("segment-", 40), "-")
	if len(name) <= 200 {
		t.Fatalf("the sample name is %d long, want more than 200", len(name))
	}
	if err := os.MkdirAll(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", name[:200]+"-3f9a1c"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd, refusal := l.Launch("/home/ada/"+strings.Repeat("segment-", 40), &protocol.DaemonTerminalAgent{IssueID: "issue-1", Brief: "brief"})
	if refusal != nil {
		t.Fatalf("launch refused: %v", refusal)
	}
	defer cmd.Cleanup()
	if !slices.Contains(cmd.Argv, "--continue") {
		t.Fatalf("no --continue for a directory Claude Code cut: %q", cmd.Argv)
	}
}

func TestClaudeProjectBucketWritesADashForEachUnitOutsideLettersAndDigits(t *testing.T) {
	for _, test := range []struct{ dir, bucket string }{
		{"/home/ada/tasks/MUL-7", "-home-ada-tasks-MUL-7"},
		{"/home/ada/tasks/MUL-7/multica--feat-x", "-home-ada-tasks-MUL-7-multica--feat-x"},
		{"/home/ada/my_repo.v2", "-home-ada-my-repo-v2"},
		{"/home/ada/任务", "-home-ada---"},
		// One character outside the basic multilingual plane is two units, so
		// two dashes.
		{"/home/ada/\U00020000", "-home-ada---"},
		{"/home/ada/a\U00020000", "-home-ada-a--"},
		{"/home/ada/tasks/", "-home-ada-tasks"},
		{"/", "-"},
		{"C:/ada/tasks", "C--ada-tasks"},
		{"C:/", "C--"},
	} {
		if got := claudeProjectBucket(test.dir); got != test.bucket {
			t.Errorf("claudeProjectBucket(%q) = %q, want %q", test.dir, got, test.bucket)
		}
	}
}

func TestLaunchRefusesWhatItCannotRun(t *testing.T) {
	l, hooks := newLauncher(t, "")
	tree := t.TempDir()
	for _, tc := range []struct {
		name string
		task *protocol.DaemonTerminalAgent
		code string
	}{
		{"no agent part", nil, protocol.TerminalErrBadRequest},
		{"no issue", &protocol.DaemonTerminalAgent{Brief: "brief"}, protocol.TerminalErrBadRequest},
		{"no brief", &protocol.DaemonTerminalAgent{IssueID: "issue-1"}, protocol.TerminalErrBadRequest},
	} {
		cmd, refusal := l.Launch(tree, tc.task)
		if cmd != nil || refusal == nil || refusal.Code != tc.code {
			t.Fatalf("%s: launch = %v, %v; want a %s refusal", tc.name, cmd, refusal, tc.code)
		}
	}
	// A stopped receiver, or no executable, is the daemon's failure to report.
	_ = hooks.Close()
	bare := NewLauncher(Config{Claude: "/opt/nowhere/claude", Hooks: hooks})
	if _, refusal := bare.Launch(tree, &protocol.DaemonTerminalAgent{IssueID: "issue-1", Brief: "brief"}); refusal == nil || refusal.Code != protocol.TerminalErrInternal {
		t.Fatalf("launch with a closed receiver = %v, want an internal refusal", refusal)
	}
	if _, refusal := NewLauncher(Config{}).Launch(tree, &protocol.DaemonTerminalAgent{IssueID: "issue-1", Brief: "brief"}); refusal == nil || refusal.Code != protocol.TerminalErrInternal {
		t.Fatalf("launch without an executable = %v, want an internal refusal", refusal)
	}
}

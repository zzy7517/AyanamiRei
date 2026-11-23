// Package agentsession starts the agent CLI for the web terminal: it turns
// the agent part of an open into the command that runs in the terminal.
//
// A session runs with the user's own Claude Code configuration. Nothing of the
// backend's task launch applies to it: no credentials, config directory or
// environment is chosen for it, and whether it resumes a conversation is left
// to what Claude Code already keeps for the directory.
package agentsession

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/multica-ai/multica/server/internal/daemon/agenthooks"
	dterminal "github.com/multica-ai/multica/server/internal/daemon/terminal"
	claudeagent "github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// settingsFileName is the --settings file inside its private directory.
const settingsFileName = "settings.json"

// skipDangerousModePermissionPrompt keeps bypassPermissions from asking for
// confirmation at the start of every session.
var skipDangerousModePermissionPrompt = []byte(`{"skipDangerousModePermissionPrompt": true}`)

// Config configures a Launcher.
type Config struct {
	// Claude is the Claude Code executable, used as given.
	Claude string
	// Hooks receives the session's lifecycle; every launch is given its own
	// token. Required: a session whose turns are not observed is refused.
	Hooks *agenthooks.Manager
	// SkillSettings is what execenv prepares for Claude, or nil when there
	// is nothing to hide. The launch's own settings are merged over it.
	SkillSettings []byte
	// Logger receives warnings; empty means slog.Default.
	Logger *slog.Logger
}

// Launcher builds the command of an agent terminal. The zero value is not
// usable; call NewLauncher. It implements dterminal.Launcher.
type Launcher struct {
	claude        string
	hooks         *agenthooks.Manager
	skillSettings []byte
}

// NewLauncher returns a launcher that starts cfg.Claude.
func NewLauncher(cfg Config) *Launcher {
	return &Launcher{claude: cfg.Claude, hooks: cfg.Hooks, skillSettings: cfg.SkillSettings}
}

// Launch returns the command for an agent terminal in cwd, the start tree's
// path: the CLI in bypass mode, with every other tree of the task that exists
// on this machine added, and the brief as an appended system prompt.
//
// The session's turn ends are reported on Hooks.TurnEnds with the issue, for
// as long as the terminal lives.
func (l *Launcher) Launch(cwd string, task *protocol.DaemonTerminalAgent) (*dterminal.Command, *dterminal.Refusal) {
	if task == nil || task.IssueID == "" || task.Brief == "" {
		return nil, refuse(protocol.TerminalErrBadRequest, "the agent open names no issue or no brief")
	}
	if l.claude == "" || l.hooks == nil {
		return nil, refuse(protocol.TerminalErrInternal, "this daemon cannot start agent sessions")
	}
	configDir, err := claudeagent.ClaudeConfigDir(nil, cwd)
	if err != nil {
		return nil, refuse(protocol.TerminalErrInternal, "Claude Code's config directory could not be resolved: "+err.Error())
	}
	token, err := l.hooks.MintToken()
	if err != nil {
		return nil, refuse(protocol.TerminalErrInternal, "the hook receiver is not running: "+err.Error())
	}
	fragment, err := agenthooks.SettingsFragment(l.hooks.Port(), token)
	if err != nil {
		return nil, refuse(protocol.TerminalErrInternal, err.Error())
	}
	settings, err := agenthooks.MergeSettings(l.skillSettings, fragment)
	if err == nil {
		settings, err = agenthooks.MergeSettings(settings, skipDangerousModePermissionPrompt)
	}
	if err != nil {
		return nil, refuse(protocol.TerminalErrInternal, err.Error())
	}
	path, cleanup, err := writeSettings(settings)
	if err != nil {
		return nil, refuse(protocol.TerminalErrInternal, "the settings file could not be written: "+err.Error())
	}

	argv := []string{l.claude, "--permission-mode", "bypassPermissions"}
	if hasSession(configDir, cwd) {
		argv = append(argv, "--continue")
	}
	for _, dir := range task.OtherDirs {
		if isDir(dir) {
			argv = append(argv, "--add-dir", dir)
		}
	}
	argv = append(argv, "--append-system-prompt", task.Brief, "--settings", path)

	l.hooks.MapIssue(cwd, task.IssueID)
	return &dterminal.Command{
		Argv: argv,
		Env:  append(os.Environ(), "TERM=xterm-256color"),
		Cleanup: func() {
			l.hooks.UnmapIssue(cwd)
			cleanup()
		},
	}, nil
}

func refuse(code, message string) *dterminal.Refusal {
	return &dterminal.Refusal{Code: code, Message: message}
}

// hasSession reports whether Claude Code already keeps a session for dir. Its
// store puts them under projects in the config directory, in the directory
// named by claudeProjectBucket.
func hasSession(configDir, dir string) bool {
	projects := filepath.Join(configDir, "projects")
	bucket := claudeProjectBucket(dir)
	if len(bucket) <= claudeBucketCut {
		return isDir(filepath.Join(projects, bucket))
	}
	// The name is stored as its first claudeBucketCut characters and a hash,
	// so only that prefix can be recognised.
	entries, err := os.ReadDir(projects)
	if err != nil {
		return false
	}
	prefix := bucket[:claudeBucketCut] + "-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) && entry.IsDir() {
			return true
		}
	}
	return false
}

// claudeBucketCut is the length at which Claude Code cuts a project directory
// name and appends a hash it does not publish.
const claudeBucketCut = 200

// claudeProjectBucket is the name Claude Code gives the directory that holds
// the sessions started in dir: the path with every character outside
// [A-Za-z0-9] as a dash, one per UTF-16 unit and runs kept, without trailing
// separators. The daemon's encodeClaudeCwd follows the same rule.
func claudeProjectBucket(dir string) string {
	path := filepath.ToSlash(dir)
	if path != "/" && !windowsDriveRoot(path) {
		path = strings.TrimRight(path, "/")
	}
	name := make([]byte, 0, len(path))
	for _, unit := range utf16.Encode([]rune(path)) {
		if unit >= '0' && unit <= '9' || unit >= 'A' && unit <= 'Z' || unit >= 'a' && unit <= 'z' {
			name = append(name, byte(unit))
		} else {
			name = append(name, '-')
		}
	}
	return string(name)
}

func windowsDriveRoot(path string) bool {
	return len(path) == 3 && path[1] == ':' && path[2] == '/' &&
		(path[0] >= 'A' && path[0] <= 'Z' || path[0] >= 'a' && path[0] <= 'z')
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// writeSettings keeps settings in a private temporary directory of its own,
// and returns the file's path and what removes that directory.
func writeSettings(settings []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "multica-agent-session-")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, settingsFileName)
	if err := os.WriteFile(path, settings, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

package agenthooks

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustFragment(t *testing.T, port int, token string) []byte {
	t.Helper()
	fragment, err := SettingsFragment(port, token)
	if err != nil {
		t.Fatalf("SettingsFragment: %v", err)
	}
	if !json.Valid(fragment) {
		t.Fatalf("SettingsFragment returned invalid JSON: %s", fragment)
	}
	return fragment
}

// fragmentCommand returns the command the fragment registers, which has to be
// the same for every event.
func fragmentCommand(t *testing.T, fragment []byte) string {
	t.Helper()
	var settings struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	if err := json.Unmarshal(fragment, &settings); err != nil {
		t.Fatalf("decode fragment: %v", err)
	}
	var command string
	for event, groups := range settings.Hooks {
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("event %s registers %d groups", event, len(groups))
		}
		if command == "" {
			command = groups[0].Hooks[0].Command
		} else if command != groups[0].Hooks[0].Command {
			t.Fatalf("event %s runs a different command", event)
		}
	}
	if command == "" {
		t.Fatal("fragment registers no command")
	}
	return command
}

func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestSettingsFragmentRegistersOnlyLifecycleHooks(t *testing.T) {
	t.Parallel()
	fragment := mustFragment(t, 41234, "TOKEN")

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(fragment, &settings); err != nil {
		t.Fatal(err)
	}
	if keys := keysOf(settings); !slices.Equal(keys, []string{"hooks"}) {
		t.Fatalf("fragment sets %v, want only hooks", keys)
	}
	var hooks map[string][]json.RawMessage
	if err := json.Unmarshal(settings["hooks"], &hooks); err != nil {
		t.Fatal(err)
	}
	want := []string{"SessionEnd", "SessionStart", "Stop", "StopFailure", "UserPromptSubmit"}
	if keys := keysOf(hooks); !slices.Equal(keys, want) {
		t.Fatalf("fragment subscribes to %v, want %v", keys, want)
	}

	for event, groups := range hooks {
		if len(groups) != 1 {
			t.Errorf("%s registers %d groups, want 1", event, len(groups))
			continue
		}
		var group map[string]json.RawMessage
		if err := json.Unmarshal(groups[0], &group); err != nil {
			t.Fatal(err)
		}
		if keys := keysOf(group); !slices.Equal(keys, []string{"hooks"}) {
			t.Errorf("%s group has keys %v, want only hooks without a matcher", event, keys)
			continue
		}
		var steps []map[string]json.RawMessage
		if err := json.Unmarshal(group["hooks"], &steps); err != nil {
			t.Fatal(err)
		}
		if len(steps) != 1 {
			t.Errorf("%s registers %d hooks, want 1", event, len(steps))
			continue
		}
		if keys := keysOf(steps[0]); !slices.Equal(keys, []string{"command", "type"}) {
			t.Errorf("%s hook has keys %v, want command and type", event, keys)
			continue
		}
		var stepType, command string
		if err := json.Unmarshal(steps[0]["type"], &stepType); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(steps[0]["command"], &command); err != nil {
			t.Fatal(err)
		}
		if stepType != "command" {
			t.Errorf("%s hook has type %q, want command", event, stepType)
		}
		for _, part := range []string{"http://127.0.0.1:41234/hook", "'Authorization: Bearer TOKEN'"} {
			if !strings.Contains(command, part) {
				t.Errorf("%s command lacks %q: %s", event, part, command)
			}
		}
	}
}

// hookResult is what running a hook command through sh produced.
type hookResult struct {
	exitErr error
	stdout  string
	stderr  string
	elapsed time.Duration
}

// runHookCommand runs command the way Claude Code runs a shell-form hook.
func runHookCommand(t *testing.T, command, input string, env ...string) hookResult {
	t.Helper()
	for _, tool := range []string{"sh", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed: %v", tool, err)
		}
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = hookEnv(env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	err := cmd.Run()
	return hookResult{exitErr: err, stdout: stdout.String(), stderr: stderr.String(), elapsed: time.Since(started)}
}

// hookEnv is the test environment without inherited proxy settings, plus extra.
func hookEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		switch strings.ToUpper(strings.SplitN(kv, "=", 2)[0]) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// wantSilent asserts what a hook owes the CLI: exit 0, no stdout, and a
// return within the given time.
func (r hookResult) wantSilent(t *testing.T, within time.Duration) {
	t.Helper()
	switch {
	case r.exitErr != nil:
		t.Errorf("hook exited with %q, want 0: %s", r.exitErr, r.stderr)
	case r.stdout != "":
		t.Errorf("hook wrote %q to stdout, which enters Claude's context", r.stdout)
	case r.elapsed > within:
		t.Errorf("hook took %s, want under %s", r.elapsed, within)
	}
}

// unusedLoopbackPort returns a 127.0.0.1 host:port nothing listens on.
func unusedLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestHookCommandDeliversToReceiver(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	command := fragmentCommand(t, mustFragment(t, m.Port(), mintToken(t, m)))
	input := hookJSON(t, eventSessionStart, "s1", map[string]any{
		"cwd": "/w", "transcript_path": "/t/s1.jsonl", "source": "startup",
	})

	runHookCommand(t, command, input).wantSilent(t, 5*time.Second)
	wantSession(t, m, "after the hook command ran", Session{
		ID: "s1", Cwd: "/w", TranscriptPath: "/t/s1.jsonl", State: StateIdle, StartedAt: setTime,
	})
}

func TestHookCommandIgnoresProxyEnvironment(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	command := fragmentCommand(t, mustFragment(t, m.Port(), mintToken(t, m)))
	input := hookJSON(t, eventUserPromptSubmit, "s1", map[string]any{"cwd": "/w"})

	deadProxy := unusedLoopbackPort(t)
	runHookCommand(t, command, input, "http_proxy="+deadProxy, "HTTP_PROXY="+deadProxy).wantSilent(t, 5*time.Second)
	wantSession(t, m, "behind a proxy variable", Session{
		ID: "s1", Cwd: "/w", State: StateRunning, StartedAt: setTime, LastTurnStartedAt: setTime,
	})
}

func TestHookCommandSurvivesClosedReceiver(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	command := fragmentCommand(t, mustFragment(t, m.Port(), mintToken(t, m)))
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	runHookCommand(t, command, "{}").wantSilent(t, 5*time.Second)
}

func TestHookCommandTimesOutAgainstStalledReceiver(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// Hold every connection open without answering.
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})

	command := fragmentCommand(t, mustFragment(t, port, "TOKEN"))
	result := runHookCommand(t, command, "{}")
	result.wantSilent(t, hookTimeout+5*time.Second)
	if result.elapsed < hookTimeout {
		t.Errorf("hook returned after %s, before its %s timeout: %s", result.elapsed, hookTimeout, result.stderr)
	}
}

func TestSettingsFragmentQuotesTokenForShell(t *testing.T) {
	t.Parallel()
	headers := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		headers <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	token := `it's "$TOKEN"`

	runHookCommand(t, fragmentCommand(t, mustFragment(t, port, token)), "{}").wantSilent(t, 5*time.Second)
	select {
	case got := <-headers:
		if want := "Bearer " + token; got != want {
			t.Errorf("hook sent %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hook command never reached the test server")
	}
}

func decodeAny(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return value
}

func TestMergeSettingsKeepsBaseKeys(t *testing.T) {
	t.Parallel()
	fragment := mustFragment(t, 41234, "TOKEN")
	base := `{"model":"opus","permissions":{"deny":["Read(~/.ssh)"]},"skillOverrides":{"review":"off"}}`

	merged, err := MergeSettings([]byte(base), fragment)
	if err != nil {
		t.Fatalf("MergeSettings: %v", err)
	}
	got := decodeAny(t, merged).(map[string]any)
	for key, want := range decodeAny(t, []byte(base)).(map[string]any) {
		if !reflect.DeepEqual(got[key], want) {
			t.Errorf("key %q: got %#v, want %#v", key, got[key], want)
		}
	}
	want := decodeAny(t, fragment).(map[string]any)["hooks"]
	if !reflect.DeepEqual(got["hooks"], want) {
		t.Errorf("hooks: got %#v, want the fragment's %#v", got["hooks"], want)
	}
}

func TestMergeSettingsAppendsToExistingHooks(t *testing.T) {
	t.Parallel()
	fragment := mustFragment(t, 41234, "TOKEN")
	base := `{"hooks":{"PreToolUse":[],"Stop":[{"matcher":"*","hooks":[{"type":"command","command":"notify"}]}]}}`

	merged, err := MergeSettings([]byte(base), fragment)
	if err != nil {
		t.Fatalf("MergeSettings: %v", err)
	}
	got := decodeAny(t, merged).(map[string]any)["hooks"].(map[string]any)
	baseHooks := decodeAny(t, []byte(base)).(map[string]any)["hooks"].(map[string]any)
	fragmentHooks := decodeAny(t, fragment).(map[string]any)["hooks"].(map[string]any)

	stop := got["Stop"].([]any)
	if len(stop) != 2 || !reflect.DeepEqual(stop[0], baseHooks["Stop"].([]any)[0]) || !reflect.DeepEqual(stop[1], fragmentHooks["Stop"].([]any)[0]) {
		t.Errorf("Stop hooks: got %#v, want the base's group followed by the fragment's", stop)
	}
	if !reflect.DeepEqual(got["PreToolUse"], []any{}) {
		t.Errorf("PreToolUse hooks: got %#v, want the base's empty list", got["PreToolUse"])
	}
	for _, event := range hookEvents[:] {
		if event == eventStop {
			continue
		}
		if !reflect.DeepEqual(got[event], fragmentHooks[event]) {
			t.Errorf("%s hooks: got %#v, want the fragment's %#v", event, got[event], fragmentHooks[event])
		}
	}
}

func TestMergeSettingsAcceptsEmptyBase(t *testing.T) {
	t.Parallel()
	fragment := mustFragment(t, 41234, "TOKEN")
	for _, base := range []string{"", "  \n", "null"} {
		merged, err := MergeSettings([]byte(base), fragment)
		if err != nil {
			t.Errorf("base %q: MergeSettings: %v", base, err)
			continue
		}
		if !reflect.DeepEqual(decodeAny(t, merged), decodeAny(t, fragment)) {
			t.Errorf("base %q: merged settings differ from the fragment: %s", base, merged)
		}
	}
}

func TestMergeSettingsRejectsMismatchedShapes(t *testing.T) {
	t.Parallel()
	fragment := string(mustFragment(t, 41234, "TOKEN"))
	for _, tc := range []struct{ name, base, fragment string }{
		{"malformed base", "{", fragment},
		{"base is not an object", "[]", fragment},
		{"base hooks is not an object", `{"hooks":[]}`, fragment},
		{"base event is not a list", `{"hooks":{"Stop":{}}}`, fragment},
		{"malformed fragment", "{}", "{"},
		{"fragment is not an object", "{}", "1"},
		{"fragment hooks is not an object", "{}", `{"hooks":[]}`},
		{"fragment event is not a list", "{}", `{"hooks":{"Stop":{}}}`},
	} {
		if merged, err := MergeSettings([]byte(tc.base), []byte(tc.fragment)); err == nil {
			t.Errorf("%s: MergeSettings returned %s, want an error", tc.name, merged)
		}
	}
}

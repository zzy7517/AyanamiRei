package agenthooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	eventSessionStart     = "SessionStart"
	eventUserPromptSubmit = "UserPromptSubmit"
	eventStop             = "Stop"
	eventStopFailure      = "StopFailure"
	eventSessionEnd       = "SessionEnd"

	// hookTimeout bounds each hook call, so a stalled receiver delays a turn
	// by seconds instead of the hook's default 60.
	hookTimeout = 2 * time.Second
)

// hookEvents are the events the settings fragment subscribes to.
var hookEvents = [...]string{
	eventSessionStart, eventUserPromptSubmit, eventStop, eventStopFailure, eventSessionEnd,
}

// hookPayload is the part of a hook's stdin JSON this package reads. Fields
// that later CLI versions add are ignored.
type hookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Event          string `json:"hook_event_name"`
	// Source is SessionStart's reason: startup, resume, clear, compact or fork.
	Source string `json:"source"`
	// StopHookActive marks a Stop that followed a Stop hook's block.
	StopHookActive bool `json:"stop_hook_active"`
	// Error and ErrorDetails are StopFailure's error type and details. In a
	// StopFailure, LastAssistantMessage is the error text the conversation
	// showed rather than Claude's response.
	Error                string `json:"error"`
	ErrorDetails         string `json:"error_details"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// errorText is what a StopFailure says about the failed turn: the error text
// the conversation showed, or else the error type with its details.
func (p hookPayload) errorText() string {
	if message := strings.TrimSpace(p.LastAssistantMessage); message != "" {
		return message
	}
	switch {
	case p.ErrorDetails == "":
		return p.Error
	case p.Error == "":
		return p.ErrorDetails
	}
	return p.Error + ": " + p.ErrorDetails
}

// hookStep and hookGroup are the settings-file shape of one command hook.
type hookStep struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type hookGroup struct {
	Hooks []hookStep `json:"hooks"`
}

// SettingsFragment returns --settings JSON whose hooks report a session's
// lifecycle to the receiver on port. Merge it into the launch's other
// settings with MergeSettings.
func SettingsFragment(port int, token string) ([]byte, error) {
	command := hookCommand(port, token)
	group := []hookGroup{{Hooks: []hookStep{{Type: "command", Command: command}}}}
	hooks := make(map[string][]hookGroup, len(hookEvents))
	for _, event := range hookEvents {
		hooks[event] = group
	}
	data, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		return nil, fmt.Errorf("encode hook settings: %w", err)
	}
	return data, nil
}

// MergeSettings returns base with fragment applied over it. A fragment key
// other than "hooks" replaces the base value. For "hooks", each event's groups
// are appended to the base's, so hooks already configured for that event keep
// running. Empty or null input counts as an empty object.
func MergeSettings(base, fragment []byte) ([]byte, error) {
	merged, err := decodeSettings(base)
	if err != nil {
		return nil, fmt.Errorf("merge settings: base: %w", err)
	}
	over, err := decodeSettings(fragment)
	if err != nil {
		return nil, fmt.Errorf("merge settings: fragment: %w", err)
	}
	for key, value := range over {
		if key == "hooks" {
			if value, err = mergeHooks(merged[key], value); err != nil {
				return nil, err
			}
		}
		merged[key] = value
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("merge settings: encode: %w", err)
	}
	return data, nil
}

// hookCommand posts stdin to the receiver. It always exits 0 and prints
// nothing: exit 2 would block the prompt or keep Claude working, and hook
// stdout is added to Claude's context. -q ignores ~/.curlrc, and --noproxy
// keeps a proxy environment variable from taking the loopback request.
func hookCommand(port int, token string) string {
	return fmt.Sprintf(
		"curl -q -sS --noproxy '*' -m %d -o /dev/null -H 'Content-Type: application/json' -H 'Expect:' -H %s --data-binary @- http://127.0.0.1:%d/hook || true",
		int(hookTimeout/time.Second), shellQuote("Authorization: Bearer "+token), port,
	)
}

// shellQuote wraps s in single quotes for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func decodeSettings(data []byte) (map[string]json.RawMessage, error) {
	settings := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(data)) == 0 {
		return settings, nil
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	// Unmarshal resets a map that receives JSON null.
	if settings == nil {
		settings = make(map[string]json.RawMessage)
	}
	return settings, nil
}

func mergeHooks(base, fragment json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(base)) == 0 {
		base = []byte("{}")
	}
	var baseHooks, fragmentHooks map[string][]json.RawMessage
	if err := json.Unmarshal(base, &baseHooks); err != nil {
		return nil, fmt.Errorf("merge settings: base hooks: %w", err)
	}
	if err := json.Unmarshal(fragment, &fragmentHooks); err != nil {
		return nil, fmt.Errorf("merge settings: fragment hooks: %w", err)
	}
	if baseHooks == nil {
		baseHooks = make(map[string][]json.RawMessage, len(fragmentHooks))
	}
	for event, groups := range fragmentHooks {
		baseHooks[event] = append(baseHooks[event], groups...)
	}
	data, err := json.Marshal(baseHooks)
	if err != nil {
		return nil, fmt.Errorf("merge settings: encode hooks: %w", err)
	}
	return data, nil
}

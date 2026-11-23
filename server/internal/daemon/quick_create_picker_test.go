package daemon

import (
	"strings"
	"testing"
)

func TestQuickCreatePickerPrompt(t *testing.T) {
	t.Parallel()
	const agentID = "282bdf4e-b502-4ca9-8fae-0c842f973e63"
	prompt := buildQuickCreatePrompt(Task{
		QuickCreatePrompt: "fix the login button color",
		AgentID:           agentID,
		Agent:             &AgentData{ID: agentID, Name: "picker-agent"},
	})
	for _, want := range []string{
		"When the user did NOT name an assignee",
		"pass `--assignee-picker`",
		"On no match or ambiguous match",
		"do NOT pass `--assignee`, `--assignee-id`, or `--assignee-picker`",
		"Unrecognized assignee: X",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, agentID) {
		t.Errorf("picker identity %s must not be copied through the model", agentID)
	}
}

func TestQuickCreatePickerEnvironment(t *testing.T) {
	t.Parallel()
	const (
		agentID = "282bdf4e-b502-4ca9-8fae-0c842f973e63"
		typeKey = "MULTICA_QUICK_CREATE_DEFAULT_ASSIGNEE_TYPE"
		idKey   = "MULTICA_QUICK_CREATE_DEFAULT_ASSIGNEE_ID"
	)
	for _, tc := range []struct {
		name       string
		prompt     string
		wantPicker bool
	}{
		{name: "ordinary task does not export picker"},
		{name: "quick-create task exports the running agent", prompt: "create work", wantPicker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := taskMulticaEnvironment(Task{
				AgentID: agentID, QuickCreatePrompt: tc.prompt,
			}, "codex", "picker-agent", "mat_fixture", "/task/config", "/task/workspaces", "https://task.example", 19514, 0, "/task/tmp")
			if !tc.wantPicker {
				for _, key := range []string{typeKey, idKey} {
					if _, ok := env[key]; ok {
						t.Errorf("non-quick-create task exported %s", key)
					}
				}
				return
			}
			if env[typeKey] != "agent" || env[idKey] != agentID {
				t.Fatalf("picker = (%q, %q), want (agent, %q)", env[typeKey], env[idKey], agentID)
			}
			layerCustomEnvAndHermesHome(env, map[string]string{typeKey: "member", idKey: "wrong-owner"}, "", nil)
			if env[typeKey] != "agent" || env[idKey] != agentID {
				t.Fatal("custom environment replaced daemon-owned picker identity")
			}
		})
	}
}

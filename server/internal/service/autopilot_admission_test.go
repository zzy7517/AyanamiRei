package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestFormatAdmissionReason(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"agent archived", "agent is archived", "assignee agent is archived"},
		{"agent no runtime", "agent has no runtime bound", "assignee agent has no runtime bound"},
		{"runtime offline retains MUL-1899 suffix", "agent runtime is offline", "agent runtime is offline at dispatch time"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatAdmissionReason(tc.raw); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// errDispatchSkipped must be distinguishable via errors.As from a wrapped
// fmt.Errorf, otherwise DispatchAutopilot's failure-vs-skip switch will treat
// it as a generic failure and the manual-trigger handler will 500. Locks in
// the contract that fixed the post-admission race (PR #2888 review fix #2).
func TestErrDispatchSkippedUnwraps(t *testing.T) {
	base := &errDispatchSkipped{reason: "assignee agent is archived"}
	wrapped := fmt.Errorf("dispatch run_only: %w", base)

	var got *errDispatchSkipped
	if !errors.As(wrapped, &got) {
		t.Fatalf("errors.As did not match errDispatchSkipped through fmt.Errorf wrap")
	}
	if got.reason != base.reason {
		t.Fatalf("reason mismatch: got %q want %q", got.reason, base.reason)
	}

	// pgx.ErrNoRows must NOT pass through the same gate — otherwise transient
	// "row not found" errors that should fail-open via shouldSkipDispatch
	// would be swallowed silently as skips at the dispatch level.
	if errors.As(pgx.ErrNoRows, &got) {
		t.Fatal("pgx.ErrNoRows wrongly satisfied errDispatchSkipped")
	}
}

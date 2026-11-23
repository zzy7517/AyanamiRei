package handler

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A daemon dials the terminal relay only after a heartbeat ack lists the
// capability, so the one place that ack is built is where the flag reaches it.
// Only the WebSocket heartbeat returns server_capabilities; the HTTP response
// keeps its older shape.
func TestDaemonHeartbeatOffersTheTerminalOnlyWithTheFlag(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "terminal heartbeat runtime", testutil.Cols{
		"daemon_id": "terminal-heartbeat-" + uuid.NewString()[:8],
		"provider":  "claude",
	})
	ctx := context.Background()
	off, _, err := testHandler.processHeartbeat(ctx, runtimeID, protocol.DaemonHeartbeatRequestPayload{})
	if err != nil {
		t.Fatalf("heartbeat with the flag off: %v", err)
	}
	if !slices.Contains(off.ServerCapabilities, protocol.DaemonCapabilityRPCV1) {
		t.Fatalf("server capabilities with the flag off = %v, want rpc-v1 kept", off.ServerCapabilities)
	}
	if slices.Contains(off.ServerCapabilities, protocol.DaemonCapabilityTerminalV1) {
		t.Fatalf("the ack offered the terminal while %s is off: %v", featureflags.TerminalV1, off.ServerCapabilities)
	}

	// The same handler, reading the flag from a provider that has it on.
	flags := featureflag.NewStaticProvider()
	flags.Set(featureflags.TerminalV1, featureflag.Rule{Default: true})
	enabled := *testHandler
	enabled.FeatureFlags = featureflag.NewService(flags)
	on, _, err := enabled.processHeartbeat(ctx, runtimeID, protocol.DaemonHeartbeatRequestPayload{})
	if err != nil {
		t.Fatalf("heartbeat with the flag on: %v", err)
	}
	if !slices.Contains(on.ServerCapabilities, protocol.DaemonCapabilityRPCV1) || !slices.Contains(on.ServerCapabilities, protocol.DaemonCapabilityTerminalV1) {
		t.Fatalf("server capabilities with the flag on = %v", on.ServerCapabilities)
	}
}

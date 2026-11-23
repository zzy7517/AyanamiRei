package daemon

import (
	"context"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/agenthooks"
)

// agentReplyTimeout bounds one report. A server that does not answer in time
// is not kept waiting for.
var agentReplyTimeout = 5 * time.Second

// reportTurnEnds reports each turn that ends in an agent terminal to the
// server, for as long as ctx lives. It is the only reader of the hook
// receiver's turn ends. A report that fails is logged and dropped: nothing is
// retried and nothing is queued, so a server that cannot be told costs one
// warning per turn, and the next turn is reported all the same.
func (d *Daemon) reportTurnEnds(ctx context.Context) {
	if d.agentHooks == nil {
		return
	}
	ends := d.agentHooks.TurnEnds()
	for {
		var end agenthooks.TurnEnd
		select {
		case <-ctx.Done():
			return
		case end = <-ends:
		}
		// A turn that ended outside an agent terminal names no issue.
		if end.IssueID == "" {
			continue
		}
		reportCtx, cancel := context.WithTimeout(ctx, agentReplyTimeout)
		err := d.client.ReportAgentReply(reportCtx, d.cfg.DaemonID, end.IssueID)
		cancel()
		if err != nil && ctx.Err() == nil {
			d.logger.Warn("agent terminal: a turn end was not reported", "issue_id", end.IssueID, "error", err)
		}
	}
}

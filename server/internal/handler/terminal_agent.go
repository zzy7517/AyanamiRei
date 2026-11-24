package handler

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/daemon/taskbrief"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/terminal"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ResolveAgent implements terminal.AgentResolver: it loads, for the user who
// asked, the task tree an agent terminal names, and renders the brief the
// agent starts with.
//
// The browser socket carries no workspace, so the issue's is the one that
// gates access, exactly as for a request: the user has to be a member of it
// and allowed to see the tree's project, and anything they are not allowed is
// reported as absent.
func (h *Handler) ResolveAgent(ctx context.Context, userID string, ref protocol.BrowserAgentRef) (*terminal.AgentLaunch, *protocol.TerminalError) {
	// An agent terminal is a terminal in a task tree: it needs both features.
	if !featureflags.TerminalV1Enabled(ctx, h.FeatureFlags) || !featureflags.TaskSessionsV1Enabled(ctx, h.FeatureFlags) {
		return nil, terminalRefusal(protocol.TerminalErrFlagDisabled, "agent terminals are off")
	}
	issueID, errIssue := util.ParseUUID(ref.IssueID)
	treeID, errTree := util.ParseUUID(ref.TreeID)
	user, errUser := util.ParseUUID(userID)
	if errIssue != nil || errTree != nil || errUser != nil {
		return nil, errAgentNotFound
	}

	issue, err := h.Queries.GetIssue(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errAgentNotFound
	}
	if err != nil {
		return nil, errAgentInternal
	}
	workspaceID := issue.WorkspaceID
	if _, err := h.getWorkspaceMember(ctx, userID, uuidToString(workspaceID)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errAgentNotFound
		}
		return nil, errAgentInternal
	}
	tree, err := h.Queries.GetIssueProjectScope(ctx, db.GetIssueProjectScopeParams{
		ID: treeID, IssueID: issueID, WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errAgentNotFound
	}
	if err != nil {
		return nil, errAgentInternal
	}
	hidden, err := projectaccess.ForPrincipals(ctx, h.Queries, workspaceID, user)
	if err != nil {
		return nil, errAgentInternal
	}
	if !hidden.CanSeeProject(tree.ProjectID) {
		return nil, errAgentNotFound
	}

	// Only a tree that is on disk, on a daemon, can be worked in.
	if tree.State != projectTreeReady || tree.Path == "" || !tree.DaemonID.Valid || tree.DaemonID.String == "" {
		return nil, terminalRefusal(protocol.TerminalErrTreeNotReady, "the tree is not built on its daemon yet")
	}
	scopes, err := h.Queries.ListIssueProjectScopes(ctx, db.ListIssueProjectScopesParams{IssueID: issueID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, errAgentInternal
	}

	// The other trees the agent may touch are those on the same daemon: a
	// path on another machine is not one it can open.
	names := projectNames{ctx: ctx, workspaceID: workspaceID, titles: map[pgtype.UUID]string{}}
	trees := []taskbrief.Tree{h.briefTree(&names, tree)}
	var otherDirs []string
	for _, scope := range scopes {
		if scope.ID == tree.ID || !sameDaemonReadyTree(scope, tree.DaemonID.String) {
			continue
		}
		otherDirs = append(otherDirs, scope.Path)
		trees = append(trees, h.briefTree(&names, scope))
	}
	brief := taskbrief.Render(taskbrief.Input{
		Task: taskbrief.Task{
			Identifier:  h.getIssuePrefix(ctx, workspaceID) + "-" + strconv.Itoa(int(issue.Number)),
			Title:       issue.Title,
			Description: issue.Description.String,
		},
		Trees:               trees,
		DescriptionMaxBytes: taskbrief.DefaultDescriptionMaxBytes,
	})
	if len(brief) > protocol.MaxTerminalBrief {
		return nil, terminalRefusal(protocol.TerminalErrInternal, "the task is too large to hand to the agent")
	}
	return &terminal.AgentLaunch{
		DaemonID: tree.DaemonID.String,
		Cwd:      tree.Path,
		Agent: protocol.DaemonTerminalAgent{
			IssueID:     ref.IssueID,
			StartTreeID: ref.TreeID,
			OtherDirs:   otherDirs,
			Brief:       brief,
		},
	}, nil
}

// projectNames names a tree's project in the brief, reading each project once.
type projectNames struct {
	ctx         context.Context
	workspaceID pgtype.UUID
	titles      map[pgtype.UUID]string
}

func (h *Handler) briefTree(names *projectNames, scope db.IssueProjectScope) taskbrief.Tree {
	title, ok := names.titles[scope.ProjectID]
	if !ok {
		if project, err := h.Queries.GetProjectInWorkspace(names.ctx, db.GetProjectInWorkspaceParams{
			ID: scope.ProjectID, WorkspaceID: names.workspaceID,
		}); err == nil {
			title = project.Title
		}
		names.titles[scope.ProjectID] = title
	}
	return taskbrief.Tree{Path: scope.Path, RepoName: title, Branch: scope.TaskBranch, BaseBranch: scope.BaseBranch}
}

// sameDaemonReadyTree reports whether scope is built on disk on daemonID.
func sameDaemonReadyTree(scope db.IssueProjectScope, daemonID string) bool {
	return scope.State == projectTreeReady && scope.Path != "" && scope.DaemonID.Valid && scope.DaemonID.String == daemonID
}

var (
	errAgentNotFound = &protocol.TerminalError{Code: protocol.TerminalErrNotFound, Message: "no such task tree"}
	// The reason is left out: it is the server's, and the log has it.
	errAgentInternal = &protocol.TerminalError{Code: protocol.TerminalErrInternal, Message: "the task tree could not be loaded"}
)

func terminalRefusal(code, message string) *protocol.TerminalError {
	return &protocol.TerminalError{Code: code, Message: message}
}

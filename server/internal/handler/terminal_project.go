package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/terminal"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ResolveProject implements terminal.ProjectResolver: it loads, for the user
// who asked, the local folder a project open names, which is one of a
// project's local_directory resources.
//
// The browser socket carries no workspace, so the resource's is the one that
// gates access, exactly as for a request: the user has to be a member of it
// and allowed to see the project, and anything they are not allowed is
// reported as absent.
//
// The path is returned as it was registered. It belongs to the daemon's
// machine, so the server neither rewrites it nor judges it by its own path
// rules; the daemon checks that the folder is there.
func (h *Handler) ResolveProject(ctx context.Context, userID, resourceID string) (*terminal.ProjectLaunch, *protocol.TerminalError) {
	if !featureflags.TerminalV1Enabled(ctx, h.FeatureFlags) {
		return nil, terminalRefusal(protocol.TerminalErrFlagDisabled, "terminals are off")
	}
	resourceUUID, errResource := util.ParseUUID(resourceID)
	user, errUser := util.ParseUUID(userID)
	if errResource != nil || errUser != nil {
		return nil, errProjectNotFound
	}

	resource, err := h.Queries.GetProjectResource(ctx, resourceUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errProjectNotFound
	}
	if err != nil {
		return nil, errProjectInternal
	}
	if resource.ResourceType != "local_directory" {
		return nil, errProjectNotFound
	}
	if _, err := h.getWorkspaceMember(ctx, userID, uuidToString(resource.WorkspaceID)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errProjectNotFound
		}
		return nil, errProjectInternal
	}
	hidden, err := projectaccess.ForPrincipals(ctx, h.Queries, resource.WorkspaceID, user)
	if err != nil {
		return nil, errProjectInternal
	}
	if !hidden.CanSeeProject(resource.ProjectID) {
		return nil, errProjectNotFound
	}

	var ref localDirectoryRef
	if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil || ref.LocalPath == "" || ref.DaemonID == "" {
		return nil, errProjectNotFound
	}
	return &terminal.ProjectLaunch{DaemonID: ref.DaemonID, Cwd: ref.LocalPath}, nil
}

var (
	// errProjectNotFound also answers a folder the resource no longer names:
	// to the browser it is the same, nothing to open.
	errProjectNotFound = &protocol.TerminalError{Code: protocol.TerminalErrProjectNotFound, Message: "no such project folder"}
	// errProjectInternal leaves the reason out: it is the server's.
	errProjectInternal = &protocol.TerminalError{Code: protocol.TerminalErrInternal, Message: "the project folder could not be loaded"}
)

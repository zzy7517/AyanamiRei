package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// localFolder registers a project's local folder on daemonID and returns the
// id of the resource, which is what a terminal open names.
func localFolder(t *testing.T, projectID, daemonID, path string) string {
	t.Helper()
	ref, err := json.Marshal(map[string]string{"daemon_id": daemonID, "local_path": path})
	if err != nil {
		t.Fatal(err)
	}
	return dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id":    projectID,
		"workspace_id":  testWorkspaceID,
		"resource_type": "local_directory",
		"resource_ref":  string(ref),
		"position":      0,
	})
}

func TestResolveProjectNamesTheFolderAProjectRegistered(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	handlerFlagsOn(t, featureflags.TerminalV1)
	projectID := dbfx.Project(t, "Terminal project")
	ctx := context.Background()

	// The path is the daemon's to read, so it comes back as it was
	// registered: anywhere on that machine, and as a Windows path too.
	for _, tc := range []struct{ daemonID, path string }{
		{"daemon-1", "/home/me/code/app"},
		{"daemon-2", "C:\\code\\app"},
	} {
		launch, refusal := testHandler.ResolveProject(ctx, testUserID, localFolder(t, projectID, tc.daemonID, tc.path))
		if refusal != nil {
			t.Fatalf("resolve %s refused: %+v", tc.path, refusal)
		}
		if launch.DaemonID != tc.daemonID || launch.Cwd != tc.path {
			t.Fatalf("resolve %s = %+v", tc.path, launch)
		}
	}
}

func TestResolveProjectRefusesWhatTheUserMayNotOpen(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	handlerFlagsOn(t, featureflags.TerminalV1)
	secret := newPrivateProjectFixture(t)
	projectID := dbfx.Project(t, "Terminal project")
	folder := localFolder(t, projectID, "daemon-1", "/home/me/code/app")
	secretFolder := localFolder(t, secret.projectID, "daemon-1", "/home/me/code/secret")
	repo := dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id":    projectID,
		"workspace_id":  testWorkspaceID,
		"resource_type": "github_repo",
		"resource_ref":  `{"url":"https://github.com/example/repo"}`,
		"position":      1,
	})
	// Only corrupt data holds a local folder without a path.
	noPath := dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id":    projectID,
		"workspace_id":  testWorkspaceID,
		"resource_type": "local_directory",
		"resource_ref":  `{"daemon_id":"daemon-1"}`,
		"position":      2,
	})
	outsider := dbfx.User(t, "Terminal outsider", "terminal-outsider-"+uuid.NewString()+"@example.test")
	ctx := context.Background()

	for _, tc := range []struct {
		name, user, resource, code string
	}{
		{"an unknown folder", testUserID, uuid.NewString(), protocol.TerminalErrProjectNotFound},
		{"a name instead of an id", testUserID, "app", protocol.TerminalErrProjectNotFound},
		{"nothing at all", testUserID, "", protocol.TerminalErrProjectNotFound},
		{"another kind of resource", testUserID, repo, protocol.TerminalErrProjectNotFound},
		{"a folder that names no path", testUserID, noPath, protocol.TerminalErrProjectNotFound},
		{"a member the private project is hidden from", secret.outsider, secretFolder, protocol.TerminalErrProjectNotFound},
		{"someone outside the workspace", outsider, folder, protocol.TerminalErrProjectNotFound},
	} {
		launch, refusal := testHandler.ResolveProject(ctx, tc.user, tc.resource)
		if launch != nil || refusal == nil || refusal.Code != tc.code {
			t.Fatalf("%s: resolve = %+v, %+v; want a %s refusal", tc.name, launch, refusal, tc.code)
		}
	}

	// A member of the private project opens its folder.
	launch, refusal := testHandler.ResolveProject(ctx, secret.insider, secretFolder)
	if refusal != nil || launch == nil || launch.DaemonID != "daemon-1" || launch.Cwd != "/home/me/code/secret" {
		t.Fatalf("a project member: resolve = %+v, %+v", launch, refusal)
	}

	// The flag off ends it, before anything is read.
	handlerFlagsOn(t)
	launch, refusal = testHandler.ResolveProject(ctx, testUserID, folder)
	if launch != nil || refusal == nil || refusal.Code != protocol.TerminalErrFlagDisabled {
		t.Fatalf("with the flag off: resolve = %+v, %+v", launch, refusal)
	}
}

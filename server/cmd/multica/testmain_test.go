package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/spf13/cobra"
)

func TestMain(m *testing.M) {
	for _, key := range []string{
		"MULTICA_AGENT_ID",
		"MULTICA_TASK_ID",
		"MULTICA_TOKEN",
		"MULTICA_DAEMON_PORT",
		"MULTICA_WORKSPACE_ID",
		"MULTICA_SERVER_URL",
		"MULTICA_TASK_CONFIG_ROOT",
		"MULTICA_TASK_WORKSPACES_ROOT",
		"MULTICA_AGENT_NAME",
		devEnvHomeEnv,
		devEnvNameEnv,
	} {
		os.Unsetenv(key)
	}

	scratchHome, err := testutil.IsolateUserHome("multica-cli-tests-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate cmd/multica test home: %v\n", err)
		os.Exit(1)
	}

	testLaunchDir, err = os.Getwd()
	if err == nil {
		err = os.Chdir(scratchHome)
	}
	if err != nil {
		_ = os.RemoveAll(scratchHome)
		fmt.Fprintf(os.Stderr, "move cmd/multica tests into scratch home: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.Chdir(os.TempDir())
	_ = os.RemoveAll(scratchHome)
	os.Exit(code)
}

var testLaunchDir string

func testCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.PersistentFlags().String("profile", "", "")
	return cmd
}

var redirectTestHome = testutil.RedirectUserHome

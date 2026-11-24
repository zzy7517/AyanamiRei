export const COMPOSIO_MCP_APPS_FLAG = "composio_mcp_apps";
export const PLUGINS_V1_FLAG = "plugins_v1";
export const DOCS_V1_FLAG = "docs_v1";
// Tasks and their trees: a task works on "project + branch" pairs, each built
// as a worktree, and an agent terminal opens inside one. Off unless the server
// turns it on; while it is off, the tree routes answer 404.
export const TASK_SESSIONS_V1_FLAG = "task_sessions_v1";
export const BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG =
  "billing_workspace_subscriptions";
// The 365-day range on the workspace dashboard (MUL-9490). Servers that
// support it publish it on by default; a missing key reads as off, so the
// range is never offered to a server that predates it.
export const DASHBOARD_HISTORY_365D_FLAG = "dashboard_history_365d";

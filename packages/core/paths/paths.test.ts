import { describe, it, expect } from "vitest";
import { paths, isGlobalPath, issueRouteRef, type IssueRouteRef } from "./paths";

describe("paths.workspace(slug)", () => {
  const ws = paths.workspace("acme");

  it("builds workspace paths with slug prefix", () => {
    expect(ws.usage()).toBe("/acme/usage");
    expect(ws.issues()).toBe("/acme/issues");
    expect(ws.issueDetail("abc-123" as IssueRouteRef)).toBe("/acme/issues/abc-123");
    expect(ws.projects()).toBe("/acme/projects");
    expect(ws.projectDetail("p1")).toBe("/acme/projects/p1");
    expect(ws.autopilots()).toBe("/acme/autopilots");
    expect(ws.autopilotDetail("a1")).toBe("/acme/autopilots/a1");
    expect(ws.agents()).toBe("/acme/agents");
    expect(ws.newAgent()).toBe("/acme/agents/new");
    expect(ws.newAgentAiSession("sess_1")).toBe("/acme/agents/new/ai/sess_1");
    expect(ws.newAgentDone("agent_1")).toBe("/acme/agents/new/done/agent_1");
    expect(ws.inbox()).toBe("/acme/inbox");
    expect(ws.chatWithAgent("agent one")).toBe(
      "/acme/chat?agent=agent%20one",
    );
    expect(ws.chatSession("session one")).toBe(
      "/acme/chat?session=session%20one",
    );
    expect(ws.runtimes()).toBe("/acme/runtimes");
    expect(ws.runtimeSettings("machine/runtime", "runtime one")).toBe(
      "/acme/runtimes/machine%2Fruntime/runtime/runtime%20one",
    );
    expect(ws.skills()).toBe("/acme/skills");
    expect(ws.skillDetail("skl_123")).toBe("/acme/skills/skl_123");
    expect(ws.settings()).toBe("/acme/settings");
    expect(ws.attachmentPreview("att_42")).toBe("/acme/attachments/att_42/preview");
  });

  it("URL-encodes special characters in ids", () => {
    expect(ws.issueDetail("id with space" as IssueRouteRef)).toBe("/acme/issues/id%20with%20space");
  });

  it("builds a deliverable link with the filename as one encoded segment", () => {
    expect(ws.issueDeliverable("MUL-1" as IssueRouteRef, "report.html")).toBe(
      "/acme/issues/MUL-1/deliverables/report.html",
    );
    expect(ws.issueDeliverable("MUL-1" as IssueRouteRef, "Q3 调研/报告 #2?.html")).toBe(
      "/acme/issues/MUL-1/deliverables/Q3%20%E8%B0%83%E7%A0%94%2F%E6%8A%A5%E5%91%8A%20%232%3F.html",
    );
  });

  it("links an issue by its identifier, and by its UUID only without one", () => {
    const issue = { id: "2f0c9b1e-6a4d-4c1b-9e7a-1d2c3b4a5f60", identifier: "MUL-7" };
    expect(ws.issueDetail(issueRouteRef(issue))).toBe("/acme/issues/MUL-7");
    expect(ws.issueDetail(issueRouteRef({ id: issue.id, identifier: null }))).toBe(`/acme/issues/${issue.id}`);
    // A bare string reaches the issue page as a second navigation when it is a
    // UUID, so the builders only take a ref.
    // @ts-expect-error -- `issue.id` is not an IssueRouteRef
    ws.issueDetail(issue.id);
    // @ts-expect-error -- neither is `issue.identifier`; go through issueRouteRef
    ws.issueDeliverable(issue.identifier, "report.html");
    // @ts-expect-error -- `identifier` must be stated, even as null
    issueRouteRef({ id: issue.id });
  });
});

describe("terminal addresses", () => {
  it("name a shell by its directory and an agent's session by its task and tree", () => {
    const ws = paths.workspace("acme");
    expect(ws.terminalIn("/srv/app")).toBe("/acme/terminal?cwd=%2Fsrv%2Fapp");
    expect(ws.terminalIn("/srv/app", { terminalId: "t-1" })).toBe("/acme/terminal?cwd=%2Fsrv%2Fapp&t=t-1");
    expect(ws.terminalIn("/srv/app", { host: "box" })).toBe("/acme/terminal?cwd=%2Fsrv%2Fapp&host=box");
    expect(ws.terminalAgent("issue-1", "tree-1")).toBe("/acme/terminal?issue=issue-1&tree=tree-1");
    expect(ws.terminalAgent("issue-1", "tree-1", "t-1")).toBe(
      "/acme/terminal?issue=issue-1&tree=tree-1&t=t-1",
    );
    expect(ws.terminalAgent("a/b", "c d")).toBe("/acme/terminal?issue=a%2Fb&tree=c%20d");
    expect(ws.terminalProject("default")).toBe("/acme/terminal?project=default");
    expect(ws.terminalProject("folder id", "t-1")).toBe("/acme/terminal?project=folder%20id&t=t-1");
  });
});

describe("paths (global)", () => {
  it("builds global paths without slug", () => {
    expect(paths.newWorkspace()).toBe("/workspaces/new");
    expect(paths.onboarding()).toBe("/onboarding");
    expect(paths.root()).toBe("/");
  });
});

describe("isGlobalPath", () => {
  it("returns true for pre-workspace routes", () => {
    expect(isGlobalPath("/workspaces/new")).toBe(true);
    expect(isGlobalPath("/onboarding")).toBe(true);
  });

  it("does not treat removed auth URLs as routes", () => {
    expect(isGlobalPath("/login")).toBe(false);
    expect(isGlobalPath("/auth/callback")).toBe(false);
    expect(isGlobalPath("/logout")).toBe(false);
    expect(isGlobalPath("/signup")).toBe(false);
  });

  it("returns false for workspace-scoped paths", () => {
    expect(isGlobalPath("/acme/issues")).toBe(false);
    expect(isGlobalPath("/")).toBe(false);
  });
});

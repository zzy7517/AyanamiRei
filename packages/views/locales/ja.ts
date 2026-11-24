import type { LocaleResources } from "@multica/core/i18n";
import jaDocs from "./ja/docs.json";
import jaCommon from "./ja/common.json";
import jaSettings from "./ja/settings.json";
import jaIssues from "./ja/issues.json";
import jaAgents from "./ja/agents.json";
import jaEditor from "./ja/editor.json";
import jaOnboarding from "./ja/onboarding.json";
import jaLabels from "./ja/labels.json";
import jaSearch from "./ja/search.json";
import jaInbox from "./ja/inbox.json";
import jaWorkspace from "./ja/workspace.json";
import jaProjects from "./ja/projects.json";
import jaAutopilots from "./ja/autopilots.json";
import jaSkills from "./ja/skills.json";
import jaChat from "./ja/chat.json";
import jaModals from "./ja/modals.json";
import jaRuntimes from "./ja/runtimes.json";
import jaLayout from "./ja/layout.json";
import jaUsage from "./ja/usage.json";
import jaUi from "./ja/ui.json";
import jaBilling from "./ja/billing.json";
import jaTerminal from "./ja/terminal.json";

const resources: LocaleResources = {
  docs: jaDocs,
  common: jaCommon,
  settings: jaSettings,
  issues: jaIssues,
  agents: jaAgents,
  editor: jaEditor,
  onboarding: jaOnboarding,
  labels: jaLabels,
  search: jaSearch,
  inbox: jaInbox,
  workspace: jaWorkspace,
  projects: jaProjects,
  autopilots: jaAutopilots,
  skills: jaSkills,
  chat: jaChat,
  modals: jaModals,
  runtimes: jaRuntimes,
  layout: jaLayout,
  usage: jaUsage,
  ui: jaUi,
  billing: jaBilling,
  terminal: jaTerminal,
};

export default resources;

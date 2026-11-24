import type { LocaleResources } from "@multica/core/i18n";
import esDocs from "./es/docs.json";
import esCommon from "./es/common.json";
import esSettings from "./es/settings.json";
import esIssues from "./es/issues.json";
import esAgents from "./es/agents.json";
import esEditor from "./es/editor.json";
import esOnboarding from "./es/onboarding.json";
import esLabels from "./es/labels.json";
import esSearch from "./es/search.json";
import esInbox from "./es/inbox.json";
import esWorkspace from "./es/workspace.json";
import esProjects from "./es/projects.json";
import esAutopilots from "./es/autopilots.json";
import esSkills from "./es/skills.json";
import esChat from "./es/chat.json";
import esModals from "./es/modals.json";
import esRuntimes from "./es/runtimes.json";
import esLayout from "./es/layout.json";
import esUsage from "./es/usage.json";
import esUi from "./es/ui.json";
import esBilling from "./es/billing.json";
import esTerminal from "./es/terminal.json";

const resources: LocaleResources = {
  docs: esDocs,
  common: esCommon,
  settings: esSettings,
  issues: esIssues,
  agents: esAgents,
  editor: esEditor,
  onboarding: esOnboarding,
  labels: esLabels,
  search: esSearch,
  inbox: esInbox,
  workspace: esWorkspace,
  projects: esProjects,
  autopilots: esAutopilots,
  skills: esSkills,
  chat: esChat,
  modals: esModals,
  runtimes: esRuntimes,
  layout: esLayout,
  usage: esUsage,
  ui: esUi,
  billing: esBilling,
  terminal: esTerminal,
};

export default resources;

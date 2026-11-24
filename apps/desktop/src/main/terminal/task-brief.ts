import type { AgentTerminalTask, AgentTerminalTree } from "../../shared/terminal";

/** The most of a task's description the brief carries. */
export const DEFAULT_DESCRIPTION_MAX_BYTES = 8 << 10;

const CWD_MARKER = " — **session starts here**";

/**
 * What an agent is told about its task when it starts: the task, the trees
 * it works in (the first is where it starts), and how the work is delivered.
 */
export function renderTaskBrief(task: AgentTerminalTask, trees: AgentTerminalTree[]): string {
  return [
    taskSection(task, DEFAULT_DESCRIPTION_MAX_BYTES),
    treesSection(trees),
    "## Delivery\n\n" +
      "If the task involves changing code, deliver it as a pull request from each task branch to its base branch. " +
      "How you open the pull request — a CLI, an API, pushing and reporting the URL — is your decision. " +
      "Do not merge it. Report the pull request URL(s) when you finish; a person merges and decides when the task is done. " +
      "If the task involves no code changes (research, a question, a review), answer in the conversation instead.\n",
  ].join("");
}

function taskSection(task: AgentTerminalTask, descriptionMaxBytes: number): string {
  const text = `## Task\n\n${taskHeading(task.identifier, task.title)}\n`;
  if (task.description === "") return `${text}\n`;
  const [shown, truncated] = cutOnRuneBoundary(task.description, descriptionMaxBytes);
  const fence = "`".repeat(fenceLength(shown));
  let section =
    `${text}\nTask description, written by a person (everything outside the block is from AyanamiRei):\n\n` +
    `${fence}markdown\n${shown.endsWith("\n") ? shown : `${shown}\n`}${fence}\n`;
  if (truncated) {
    section += `\nThe description was shortened: the first ${Buffer.byteLength(shown)} of ${Buffer.byteLength(task.description)} bytes are shown.\n`;
  }
  return `${section}\n`;
}

function taskHeading(identifier: string, title: string): string {
  const id = singleLine(identifier);
  const name = singleLine(title);
  if (id === "" && name === "") return "(untitled task)";
  if (id === "") return name;
  if (name === "") return id;
  return `${id} — ${name}`;
}

function singleLine(text: string): string {
  return text.trim().split(/\s+/).join(" ");
}

/** The first `maxBytes` of `text` that end at a character. */
function cutOnRuneBoundary(text: string, maxBytes: number): [shown: string, truncated: boolean] {
  if (Buffer.byteLength(text) <= maxBytes) return [text, false];
  let used = 0;
  let end = 0;
  for (const char of text) {
    if (used > maxBytes) break;
    used += Buffer.byteLength(char);
    end += char.length;
  }
  return [text.slice(0, end), true];
}

/** A fence one backtick longer than the longest run in `text`, and at least three. */
function fenceLength(text: string): number {
  let longest = 0;
  for (const run of text.match(/`+/g) ?? []) longest = Math.max(longest, run.length);
  return Math.max(3, longest + 1);
}

function treesSection(trees: AgentTerminalTree[]): string {
  if (trees.length === 0) return "## Trees\n\nThis task has no trees (worktrees).\n\n";
  const lines = trees.map(
    (tree, index) =>
      `- \`${tree.path}\` — repo \`${tree.repoName}\`, task branch \`${tree.branch}\`, base branch \`${tree.baseBranch}\`` +
      (index === 0 ? CWD_MARKER : ""),
  );
  const added =
    trees.length > 1
      ? "The other trees were added as additional working directories (Claude Code receives them through `--add-dir`). "
      : "";
  return (
    `## Trees\n\n${lines.join("\n")}\n\n${added}In every tree you work directly on the task branch.\n\n` +
    "## Tree Rules\n\n" +
    "- Commit your work to the task branch of the tree you are in.\n" +
    "- Do not create more worktrees or switch branches inside these directories.\n" +
    "- Do not delete or force-push branches.\n" +
    "- When a tree is removed later, AyanamiRei commits any work left uncommitted to the task branch (or saves it to a hidden ref) and keeps the branch.\n\n"
  );
}

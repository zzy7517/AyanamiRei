// Package taskbrief renders the text AyanamiRei passes to an agent CLI at
// session start: which task the session belongs to, where its worktrees are,
// what may be done to them, and how the work is delivered.
package taskbrief

import (
	"fmt"
	"strings"
)

// Tree is one worktree of the task.
type Tree struct {
	Path       string // absolute path of the worktree
	RepoName   string // display name of the repository
	Branch     string // task branch checked out in the worktree
	BaseBranch string // branch the tree was created from; the delivery target
}

// Task is the record the trees belong to.
type Task struct {
	Identifier  string // issue identifier such as MUL-123; may be empty
	Title       string
	Description string // may be empty, may be long, may be markdown
}

// Input is everything Render needs.
type Input struct {
	Task  Task
	Trees []Tree // the session was started in Trees[Cwd]
	Cwd   int    // index into Trees; treated as 0 when out of range

	// DescriptionMaxBytes caps how many bytes of Task.Description are
	// included. A longer description is cut on a rune boundary.
	// DefaultDescriptionMaxBytes applies when it is zero or negative.
	DescriptionMaxBytes int
}

// DefaultDescriptionMaxBytes is the description budget used when
// Input.DescriptionMaxBytes is zero or negative.
const DefaultDescriptionMaxBytes = 8 << 10

// cwdMarker ends the line of the tree the session starts in.
const cwdMarker = " — **session starts here**"

// Render returns the text passed to the agent CLI at session start. It is
// deterministic and accepts the zero value and out-of-range input.
func Render(in Input) string {
	var b strings.Builder
	writeTask(&b, in.Task, descriptionLimit(in.DescriptionMaxBytes))
	writeTrees(&b, in.Trees, cwdIndex(in.Cwd, len(in.Trees)))
	writeDelivery(&b)
	return b.String()
}

func writeTask(b *strings.Builder, t Task, maxBytes int) {
	b.WriteString("## Task\n\n")
	b.WriteString(taskHeading(t.Identifier, t.Title))
	b.WriteString("\n")
	if t.Description == "" {
		b.WriteString("\n")
		return
	}
	shown, truncated := cutOnRuneBoundary(t.Description, maxBytes)
	// One backtick longer than any run in the description, so a code fence a
	// person wrote cannot close this block and let the rest of the description
	// read as instructions.
	fence := strings.Repeat("`", fenceLength(shown))
	b.WriteString("\nTask description, written by a person (everything outside the block is from AyanamiRei):\n\n")
	b.WriteString(fence + "markdown\n")
	b.WriteString(shown)
	if !strings.HasSuffix(shown, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(fence + "\n")
	if truncated {
		// Outside the block: the note is ours, the text inside is a person's.
		fmt.Fprintf(b, "\nThe description was shortened: the first %d of %d bytes are shown.\n", len(shown), len(t.Description))
	}
	b.WriteString("\n")
}

// taskHeading names the task on one line. The issue API rejects only an empty
// title, so a line break in one would add lines to the brief outside the
// description block, where the label says everything comes from AyanamiRei.
func taskHeading(identifier, title string) string {
	identifier, title = singleLine(identifier), singleLine(title)
	switch {
	case identifier == "" && title == "":
		return "(untitled task)"
	case identifier == "":
		return title
	case title == "":
		return identifier
	}
	return identifier + " — " + title
}

func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// cutOnRuneBoundary returns the longest prefix of s that fits in maxBytes
// without splitting a rune, and whether anything was cut. It walks rune starts
// instead of re-encoding, so bytes that are not valid UTF-8 survive as they are.
func cutOnRuneBoundary(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	end := 0
	for i := range s {
		if i > maxBytes {
			break
		}
		end = i
	}
	return s[:end], true
}

func descriptionLimit(n int) int {
	if n <= 0 {
		return DefaultDescriptionMaxBytes
	}
	return n
}

// fenceLength is the shortest backtick fence that no line of s can close.
func fenceLength(s string) int {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return max(3, longest+1)
}

func cwdIndex(cwd, n int) int {
	if cwd < 0 || cwd >= n {
		return 0
	}
	return cwd
}

func writeTrees(b *strings.Builder, trees []Tree, cwd int) {
	b.WriteString("## Trees\n\n")
	if len(trees) == 0 {
		b.WriteString("This task has no trees (worktrees).\n\n")
		return
	}
	for i, t := range trees {
		fmt.Fprintf(b, "- `%s` — repo `%s`, task branch `%s`, base branch `%s`", t.Path, t.RepoName, t.Branch, t.BaseBranch)
		if i == cwd {
			b.WriteString(cwdMarker)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if len(trees) > 1 {
		b.WriteString("The other trees were added as additional working directories (Claude Code receives them through `--add-dir`). ")
	}
	b.WriteString("In every tree you work directly on the task branch.\n\n")
	b.WriteString("## Tree Rules\n\n")
	b.WriteString("- Commit your work to the task branch of the tree you are in.\n")
	b.WriteString("- Do not create more worktrees or switch branches inside these directories.\n")
	b.WriteString("- Do not delete or force-push branches.\n")
	b.WriteString("- When a tree is removed later, AyanamiRei commits any work left uncommitted to the task branch (or saves it to a hidden ref) and keeps the branch.\n\n")
}

func writeDelivery(b *strings.Builder) {
	b.WriteString("## Delivery\n\n")
	b.WriteString("If the task involves changing code, deliver it as a pull request from each task branch to its base branch. ")
	b.WriteString("How you open the pull request — a CLI, an API, pushing and reporting the URL — is your decision. ")
	b.WriteString("Do not merge it. Report the pull request URL(s) when you finish; a person merges and decides when the task is done. ")
	b.WriteString("If the task involves no code changes (research, a question, a review), answer in the conversation instead.\n")
}

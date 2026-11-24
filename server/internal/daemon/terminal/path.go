package terminal

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Refusal is why the host did not do what the server asked. Code is one of
// the protocol's terminal error codes and reaches the browser unchanged;
// Message is shown to the person at the browser.
type Refusal struct {
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

func refuse(code, message string) *Refusal {
	return &Refusal{Code: code, Message: message}
}

// ResolveCwd maps the directory an open asked for onto the directory the
// shell runs in: an existing directory this process may enter, inside one of
// roots (a root included), with every symlink followed on both sides. A
// symlink that leads out of every root is therefore refused, and a root that
// is itself a symlink works. A root that is not a directory on this machine,
// such as a task tree root before the first task, allows nothing.
func ResolveCwd(roots []string, cwd string) (string, *Refusal) {
	if !filepath.IsAbs(cwd) || strings.ContainsRune(cwd, 0) {
		return "", refuse(protocol.TerminalErrBadRequest, "cwd must be an absolute path")
	}
	dir, err := util.ResolveSymlinks(cwd)
	if err != nil || !isDir(dir) {
		return "", refuse(protocol.TerminalErrBadRequest, "not an existing directory: "+cwd)
	}
	if !canEnter(dir) {
		return "", refuse(protocol.TerminalErrBadRequest, "not a directory this daemon may enter: "+cwd)
	}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			continue
		}
		resolvedRoot, err := util.ResolveSymlinks(root)
		if err != nil || !isDir(resolvedRoot) {
			continue
		}
		if within(resolvedRoot, dir) {
			return dir, nil
		}
	}
	return "", refuse(protocol.TerminalErrPathNotAllowed, "outside the task and project folders")
}

// within reports whether dir is root or below it, both resolved.
func within(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

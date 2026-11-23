package terminal

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RootEnv is the daemon setting that names the tree terminals may run in.
const RootEnv = "MULTICA_TERMINAL_ROOT"

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
// shell runs in: an existing directory this process may enter, inside root
// (root included), with every symlink followed on both sides. A symlink that
// leads out of the root is therefore refused, and a root that is itself a
// symlink works.
func ResolveCwd(root, cwd string) (string, *Refusal) {
	if root == "" {
		return "", refuse(protocol.TerminalErrTerminalRootUnconfigured, RootEnv+" is not set on this daemon")
	}
	if !filepath.IsAbs(root) {
		return "", refuse(protocol.TerminalErrTerminalRootUnconfigured, RootEnv+" must be an absolute path")
	}
	if !filepath.IsAbs(cwd) || strings.ContainsRune(cwd, 0) {
		return "", refuse(protocol.TerminalErrBadRequest, "cwd must be an absolute path")
	}
	resolvedRoot, err := util.ResolveSymlinks(root)
	if err != nil || !isDir(resolvedRoot) {
		return "", refuse(protocol.TerminalErrTerminalRootUnconfigured,
			RootEnv+" does not name an accessible directory")
	}
	dir, err := util.ResolveSymlinks(cwd)
	if err != nil || !isDir(dir) {
		return "", refuse(protocol.TerminalErrBadRequest, "not an existing directory: "+cwd)
	}
	if !canEnter(dir) {
		return "", refuse(protocol.TerminalErrBadRequest, "not a directory this daemon may enter: "+cwd)
	}
	if rel, err := filepath.Rel(resolvedRoot, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", refuse(protocol.TerminalErrPathNotAllowed, "outside the terminal root")
	}
	return dir, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

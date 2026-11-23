package tasktree

import (
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
)

// IsUnderRoot reports whether path is root itself or lies below it, comparing
// both with every symlink resolved. A path that cannot be resolved — it does
// not exist — is not under the root.
//
// It is for paths that came from outside the library, such as a tree path in
// a request: `<root>/../elsewhere` and a link leading out of the root both
// read as outside.
func IsUnderRoot(root, path string) bool {
	resolvedRoot, err := util.ResolveSymlinks(root)
	if err != nil {
		return false
	}
	resolvedPath, err := util.ResolveSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

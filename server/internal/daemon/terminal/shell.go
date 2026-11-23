package terminal

import (
	"crypto/rand"
	"errors"
	"os"
)

// errUnsupportedPlatform is what opening a terminal answers on a platform
// without the pseudo-terminal implementation.
var errUnsupportedPlatform = errors.New("terminals are not supported on this platform")

// Exit is how a shell ended: with an exit code, or by a signal that killed it.
type Exit struct {
	// Code is the exit code, or nil when a signal ended the process.
	Code *int
	// Signal names the signal that ended the process, such as "SIGKILL".
	Signal string
}

// DefaultShell is the program an interactive terminal runs when none is
// configured: $SHELL when it names an executable file, else /bin/sh.
func DefaultShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		if info, err := os.Stat(shell); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return shell
		}
	}
	return "/bin/sh"
}

// newID mints the unguessable id the server will name a terminal by.
func newID() string {
	return rand.Text()
}

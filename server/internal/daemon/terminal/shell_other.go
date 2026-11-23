//go:build !linux

package terminal

import (
	"time"
)

// Every other platform answers that it cannot host a terminal. The daemon
// still compiles, connects, and reports unsupported_platform for an open.

type shell struct {
	done chan struct{}
}

func startProcess(argv []string, dir string, cols, rows int, env []string) (*shell, error) {
	return nil, errUnsupportedPlatform
}

func (s *shell) pid() int                      { return 0 }
func (s *shell) Read(p []byte) (int, error)    { return 0, errUnsupportedPlatform }
func (s *shell) Write(p []byte) (int, error)   { return 0, errUnsupportedPlatform }
func (s *shell) Resize(cols, rows int) error   { return errUnsupportedPlatform }
func (s *shell) pending() int                  { return 0 }
func (s *shell) Done() <-chan struct{}         { return s.done }
func (s *shell) Exit() Exit                    { return Exit{} }
func (s *shell) Close() error                  { return errUnsupportedPlatform }
func (s *shell) Terminate(grace time.Duration) {}
func canEnter(dir string) bool                 { return false }

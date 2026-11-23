//go:build linux

package terminal

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// pty is a Linux pseudo-terminal. master is the end this process reads and
// writes; tty names the slave the shell runs on.
type pty struct {
	master *os.File
	tty    string
}

func openPTY() (*pty, error) {
	// os.OpenFile rather than os.NewFile on a raw descriptor: only the former
	// registers the file with the poller, and only for a pollable file does
	// Close interrupt a Read that is waiting for output.
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	p := &pty{master: master}
	// The slave comes locked, so that its owner can set its mode before any
	// other process opens it. The descriptor is borrowed through
	// SyscallConn: File.Fd would switch it to blocking mode, which ties up
	// a thread for as long as a read waits for output.
	conn, err := master.SyscallConn()
	if err != nil {
		master.Close()
		return nil, err
	}
	var setupErr error
	err = conn.Control(func(fd uintptr) {
		unlocked := int32(0)
		if setupErr = ioctlPtr(int(fd), unix.TIOCSPTLCK, unsafe.Pointer(&unlocked)); setupErr != nil {
			return
		}
		number, err := unix.IoctlGetUint32(int(fd), unix.TIOCGPTN)
		if setupErr = err; err == nil {
			p.tty = "/dev/pts/" + strconv.FormatUint(uint64(number), 10)
		}
	})
	if setupErr == nil {
		setupErr = err
	}
	if setupErr != nil {
		master.Close()
		return nil, setupErr
	}
	return p, nil
}

// ioctlPtr is an ioctl whose argument is passed by reference. x/sys/unix
// has typed helpers for the requests it knows; this one it does not.
func ioctlPtr(fd int, req uint, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// ioctl runs op with the master's descriptor while that descriptor is
// certainly open, whatever Close does meanwhile.
func (p *pty) ioctl(op func(fd int) error) error {
	conn, err := p.master.SyscallConn()
	if err != nil {
		return err
	}
	var opErr error
	if err := conn.Control(func(fd uintptr) { opErr = op(int(fd)) }); err != nil {
		return err
	}
	return opErr
}

// resize sets the terminal size. The kernel turns a change into SIGWINCH for
// the shell's foreground process group, which is what makes a full-screen
// program redraw.
func (p *pty) resize(cols, rows int) error {
	return p.ioctl(func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
	})
}

// pending is FIONREAD on the master: the program's output that was not read
// yet. TIOCOUTQ would tell of the other direction, the input waiting for the
// program.
func (p *pty) pending() int {
	n := 0
	p.ioctl(func(fd int) error {
		n, _ = unix.IoctlGetInt(fd, unix.TIOCINQ)
		return nil
	})
	return n
}

// foregroundGroup returns the process group in the terminal's foreground: the
// shell's own, or that of the job it is running. It is asked of the master:
// the group is the slave's, and only a process the slave controls (the shell
// and its jobs) may ask the slave itself.
func (p *pty) foregroundGroup() int {
	pgid := 0
	p.ioctl(func(fd int) error {
		pgid, _ = unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		return nil
	})
	return pgid
}

// canEnter reports whether this process may make dir its working directory.
func canEnter(dir string) bool {
	return unix.Access(dir, unix.R_OK|unix.X_OK) == nil
}

// exitStatus reads how a waited process ended. exec reports a process killed
// by a signal with exit code -1; the signal itself is on the wait status.
func exitStatus(err error) Exit {
	var exit Exit
	if err == nil {
		code := 0
		exit.Code = &code
		return exit
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			exit.Signal = unix.SignalName(ws.Signal())
			return exit
		}
		code := ee.ExitCode()
		exit.Code = &code
	}
	return exit
}

// shell is one interactive shell. It is a session leader whose controlling
// terminal is the pseudo-terminal's slave, which is what makes job control,
// window-size signals and the hangup on close behave as in any terminal
// emulator.
type shell struct {
	pty  *pty
	cmd  *exec.Cmd
	done chan struct{}
	exit Exit // valid once done is closed
}

// startProcess runs argv, an interactive shell or the agent CLI, with dir as
// its working directory, on a new pseudo-terminal of cols by rows. env is the
// process's whole environment.
func startProcess(argv []string, dir string, cols, rows int, env []string) (*shell, error) {
	p, err := openPTY()
	if err != nil {
		return nil, err
	}
	// O_NOCTTY keeps the daemon itself from acquiring the slave as its
	// controlling terminal before the child does.
	slave, err := os.OpenFile(p.tty, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		p.master.Close()
		return nil, err
	}
	defer slave.Close()
	if err := p.resize(cols, rows); err != nil {
		p.master.Close()
		return nil, err
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	// Setsid makes the shell a session leader, and Setctty makes its stdin
	// the controlling terminal of that session.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		p.master.Close()
		return nil, err
	}

	s := &shell{pty: p, cmd: cmd, done: make(chan struct{})}
	go func() {
		s.exit = exitStatus(cmd.Wait())
		close(s.done)
	}()
	return s, nil
}

func (s *shell) pid() int { return s.cmd.Process.Pid }

// Read returns the next chunk of terminal output. It blocks until there is
// some and fails once the terminal is closed or every process on the slave
// side has exited.
func (s *shell) Read(p []byte) (int, error) { return s.pty.master.Read(p) }

// Write sends keyboard input to the terminal. It blocks while the terminal's
// input queue is full, exactly as the kernel would block a writer on a tty.
func (s *shell) Write(p []byte) (int, error) { return s.pty.master.Write(p) }

// pending returns how many bytes the program wrote that were not read yet.
func (s *shell) pending() int { return s.pty.pending() }

func (s *shell) Resize(cols, rows int) error { return s.pty.resize(cols, rows) }

// Done is closed once the shell has exited; Exit is valid after that.
func (s *shell) Done() <-chan struct{} { return s.done }

func (s *shell) Exit() Exit { return s.exit }

// Close releases the terminal. Every process still on it gets a hangup, and
// a blocked Read or Write fails.
func (s *shell) Close() error { return s.pty.master.Close() }

// Terminate ends the shell and the job it runs in the foreground, then
// SIGKILLs whatever is left once the shell has exited or grace has passed.
// It returns without waiting for the kill to land, so a process stuck in the
// kernel cannot hold the caller.
//
// The first round is both SIGHUP and SIGTERM. SIGHUP is what closing a
// terminal sends, and the only one an interactive bash answers: it ignores
// SIGTERM. SIGTERM lets a service running in the terminal end gracefully.
// The shell can exit on the hangup while a job of its own ignores both, so
// its exit alone does not mean the terminal is empty.
func (s *shell) Terminate(grace time.Duration) {
	group := s.cmd.Process.Pid
	foreground := s.pty.foregroundGroup()
	signal := func(sig syscall.Signal) {
		syscall.Kill(-group, sig)
		if foreground > 0 && foreground != group {
			syscall.Kill(-foreground, sig)
		}
	}
	signal(syscall.SIGHUP)
	signal(syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-s.done:
		if groupEmpty(group) && groupEmpty(foreground) {
			return
		}
		<-timer.C
	case <-timer.C:
	}
	signal(syscall.SIGKILL)
}

// groupEmpty reports whether no process is left in a process group.
func groupEmpty(pgid int) bool {
	return pgid <= 0 || syscall.Kill(-pgid, 0) == syscall.ESRCH
}

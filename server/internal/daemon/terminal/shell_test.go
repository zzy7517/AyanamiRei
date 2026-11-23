//go:build linux

package terminal

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const shellDeadline = 5 * time.Second

// startTestShell starts the machine's sh on a fresh pseudo-terminal, in a
// directory of its own, and returns both.
func startTestShell(t *testing.T, cols, rows int) (*shell, string) {
	t.Helper()
	dir := t.TempDir()
	sh, err := startProcess([]string{"/bin/sh"}, dir, cols, rows, terminalEnv())
	if err != nil {
		t.Fatalf("start sh: %v", err)
	}
	t.Cleanup(func() { sh.Close() })
	return sh, dir
}

func writeInput(t *testing.T, sh *shell, input string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := sh.Write([]byte(input))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write %q: %v", input, err)
		}
	case <-time.After(shellDeadline):
		t.Fatalf("write %q did not finish", input)
	}
}

type shellRead struct {
	buf []byte
	err error
}

// readChunk reads one chunk of terminal output, or fails the test if none
// arrives or the read fails.
func readChunk(t *testing.T, sh *shell) []byte {
	t.Helper()
	result := make(chan shellRead, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := sh.Read(buf)
		result <- shellRead{buf[:n], err}
	}()
	select {
	case r := <-result:
		if r.err != nil {
			t.Fatalf("read: %v", r.err)
		}
		return r.buf
	case <-time.After(shellDeadline):
		t.Fatal("read blocked: no terminal output arrived")
		return nil
	}
}

// readUntil reads terminal output until it contains want, and returns all
// the output read on the way.
func readUntil(t *testing.T, sh *shell, want string) string {
	t.Helper()
	var out bytes.Buffer
	deadline := time.Now().Add(shellDeadline)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("terminal output never contained %q; got %q", want, out.String())
		}
		out.Write(readChunk(t, sh))
	}
	return out.String()
}

// readValue reads terminal output until a line of the form want<value>\r\n
// appears, and returns the value. The marker must not occur in the echoed
// input, or the echo of the command is what gets read.
func readValue(t *testing.T, sh *shell, want string) string {
	t.Helper()
	out := readUntil(t, sh, want)
	for {
		rest := out[strings.LastIndex(out, want)+len(want):]
		if end := strings.Index(rest, "\r\n"); end >= 0 {
			return rest[:end]
		}
		out += string(readChunk(t, sh))
	}
}

func waitDone(t *testing.T, sh *shell) {
	t.Helper()
	select {
	case <-sh.Done():
	case <-time.After(shellDeadline):
		t.Fatal("the shell did not exit")
	}
}

// processRunning reports whether pid names a live process; a zombie has
// already ended as far as this is concerned.
func processRunning(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	state := data[bytes.LastIndexByte(data, ')')+2:]
	return len(state) > 0 && state[0] != 'Z'
}

func waitForGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(shellDeadline)
	for processRunning(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForPidFile returns the process id a program wrote to path.
func waitForPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(shellDeadline)
	for {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no process id appeared in %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestShellRunsCommandsAndReportsTheExitCode(t *testing.T) {
	sh, dir := startTestShell(t, 80, 24)

	writeInput(t, sh, "echo hello from the terminal\n")
	readUntil(t, sh, "hello from the terminal")

	// The shell runs where it was told to, with the terminal type asked for.
	writeInput(t, sh, "pwd; echo term=$TERM\n")
	if out := readUntil(t, sh, "term=xterm-256color"); !strings.Contains(out, dir) {
		t.Fatalf("the shell is not running in %s: %q", dir, out)
	}

	writeInput(t, sh, "exit 3\n")
	waitDone(t, sh)
	exit := sh.Exit()
	if exit.Code == nil || *exit.Code != 3 || exit.Signal != "" {
		t.Fatalf("exit = %+v, want code 3", exit)
	}
}

func TestShellIsASessionLeaderWithJobControl(t *testing.T) {
	sh, _ := startTestShell(t, 80, 24)
	pid := sh.pid()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("read stat: %v", err)
	}
	// pid (comm) state ppid pgrp session tty_nr ...
	fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+2:]))
	pgrp, session, ttyNr := fields[2], fields[3], fields[4]
	if pgrp != strconv.Itoa(pid) || session != strconv.Itoa(pid) {
		t.Fatalf("shell %d leads process group %s in session %s; a terminal's shell must lead both", pid, pgrp, session)
	}
	if ttyNr == "0" {
		t.Fatal("the shell has no controlling terminal")
	}

	// A job the shell starts leads a process group of its own, which only a
	// shell with a controlling terminal sets up. $$ is the job's sh, and the
	// fifth field of its stat is its process group.
	// The marker is assembled in the command, so the echo of the typed line
	// does not match it.
	writeInput(t, sh, "sh -c 'o=ob; echo j${o}=$(cut -d\" \" -f5 /proc/$$/stat):$$'\n")
	job := readValue(t, sh, "job=")
	pgid, leader, _ := strings.Cut(job, ":")
	if leader == "" || pgid != leader || leader == strconv.Itoa(pid) {
		t.Fatalf("job process group:pid = %q, want a group of its own, led by itself", job)
	}
}

func TestShellResizeSetsTheTerminalSize(t *testing.T) {
	sh, _ := startTestShell(t, 80, 24)
	writeInput(t, sh, "stty size\n")
	readUntil(t, sh, "24 80")

	if err := sh.Resize(100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}
	writeInput(t, sh, "stty size\n")
	out := readUntil(t, sh, "30 100")
	if !strings.Contains(out, "30 100") {
		t.Fatalf("size after resize: %q", out)
	}
}

func TestShellTerminateEndsTheForegroundJob(t *testing.T) {
	sh, _ := startTestShell(t, 80, 24)
	pidFile := filepath.Join(t.TempDir(), "pid")
	writeInput(t, sh, "sh -c 'echo $$ > "+pidFile+"; exec sleep 60'\n")
	job := waitForPidFile(t, pidFile)
	if !processRunning(job) {
		t.Fatalf("job %d is not running", job)
	}

	sh.Terminate(closeGrace)
	waitDone(t, sh)
	waitForGone(t, job)
	if exit := sh.Exit(); exit.Code != nil || exit.Signal == "" {
		t.Fatalf("exit = %+v, want a signal", exit)
	}
}

func TestShellTerminateKillsWhatIgnoresTheFirstSignals(t *testing.T) {
	sh, dir := startTestShell(t, 80, 24)
	// A job that ignores both SIGHUP and SIGTERM outlives the grace period
	// and must still be killed.
	pidFile := filepath.Join(dir, "ignored.pid")
	writeInput(t, sh, "sh -c 'trap \"\" HUP TERM; echo $$ > ignored.pid; while :; do sleep 1; done'\n")
	job := waitForPidFile(t, pidFile)
	sh.Terminate(200 * time.Millisecond)
	waitDone(t, sh)
	waitForGone(t, job)
}

func TestShellCloseUnblocksAReader(t *testing.T) {
	sh, _ := startTestShell(t, 80, 24)
	read := make(chan error, 1)
	go func() {
		// The shell may have written its prompt by now; only the read that
		// finds the terminal gone matters.
		buf := make([]byte, 4096)
		for {
			if _, err := sh.Read(buf); err != nil {
				read <- err
				return
			}
		}
	}()
	// The reader has to be waiting when the terminal closes; otherwise it
	// fails on its own for the terminal already being closed.
	time.Sleep(100 * time.Millisecond)
	if err := sh.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("a read on a closed terminal returned no error")
		}
	case <-time.After(shellDeadline):
		t.Fatal("Close did not interrupt a blocked Read")
	}
}

func TestExitStatus(t *testing.T) {
	if got := exitStatus(nil); got.Code == nil || *got.Code != 0 {
		t.Fatalf("success = %+v, want code 0", got)
	}
	err := exec.Command("/bin/sh", "-c", "exit 7").Run()
	if got := exitStatus(err); got.Code == nil || *got.Code != 7 {
		t.Fatalf("exit 7 = %+v", got)
	}
	err = exec.Command("/bin/sh", "-c", "kill -9 $$").Run()
	if got := exitStatus(err); got.Code != nil || got.Signal != "SIGKILL" {
		t.Fatalf("killed = %+v, want SIGKILL and no code", got)
	}
	if got := exitStatus(errors.New("not a process")); got.Code != nil || got.Signal != "" {
		t.Fatalf("unrelated error = %+v", got)
	}
}

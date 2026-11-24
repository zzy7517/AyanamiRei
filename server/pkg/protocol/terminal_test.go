package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTerminalBinaryRoundTrip(t *testing.T) {
	payload := []byte("\x1b[?2004hecho hello\r\n\x00\xff")
	frame := EncodeTerminalBinary("term-1", payload)
	if len(frame) != 2+len("term-1")+len(payload) {
		t.Fatalf("frame length %d", len(frame))
	}
	id, got, err := DecodeTerminalBinary(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if id != "term-1" {
		t.Fatalf("terminal id %q", id)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload %q", got)
	}
	if &got[0] != &frame[2+len("term-1")] {
		t.Fatal("payload should alias the frame")
	}
}

func TestTerminalBinaryEmptyPayload(t *testing.T) {
	id, got, err := DecodeTerminalBinary(EncodeTerminalBinary("t", nil))
	if err != nil || id != "t" || len(got) != 0 {
		t.Fatalf("id=%q payload=%v err=%v", id, got, err)
	}
}

func TestDecodeTerminalBinaryRejectsMalformed(t *testing.T) {
	longID := strings.Repeat("a", MaxTerminalIDLen+1)
	cases := []struct {
		name  string
		frame []byte
	}{
		{"empty", nil},
		{"shorter than header", []byte{0x00}},
		{"zero-length id", EncodeTerminalBinary("", []byte("x"))},
		{"id longer than the frame", []byte{0x00, 0x05, 'a'}},
		{"id longer than the bound", EncodeTerminalBinary(longID, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := DecodeTerminalBinary(tc.frame); err == nil {
				t.Fatalf("decode(% x) succeeded, want an error", tc.frame)
			}
		})
	}
}

func TestDaemonTerminalMessageWireShape(t *testing.T) {
	// The daemon and the server are the only two peers, but both sides pin
	// the encoding so a field rename cannot pass silently.
	zero := 0
	cases := []struct {
		name string
		in   DaemonTerminalMessage
		want string
	}{
		{
			"open",
			DaemonTerminalMessage{Type: "open", RequestID: "r1", Cwd: "/srv", Cols: 120, Rows: 40},
			`{"type":"open","request_id":"r1","cwd":"/srv","cols":120,"rows":40}`,
		},
		{
			"attach",
			DaemonTerminalMessage{Type: "attach", RequestID: "r2", TerminalID: "t1"},
			`{"type":"attach","request_id":"r2","terminal_id":"t1"}`,
		},
		{
			"resize",
			DaemonTerminalMessage{Type: "resize", TerminalID: "t1", Cols: 80, Rows: 25},
			`{"type":"resize","terminal_id":"t1","cols":80,"rows":25}`,
		},
		{
			"ready",
			DaemonTerminalMessage{Type: "ready", RequestID: "r1", TerminalID: "t1", PID: 4242, Cols: 120, Rows: 40},
			`{"type":"ready","request_id":"r1","terminal_id":"t1","cols":120,"rows":40,"pid":4242}`,
		},
		{
			"exit with code",
			DaemonTerminalMessage{Type: "exit", TerminalID: "t1", ExitCode: &zero},
			`{"type":"exit","terminal_id":"t1","exit_code":0}`,
		},
		{
			"exit by signal",
			DaemonTerminalMessage{Type: "exit", TerminalID: "t1", Signal: "SIGKILL"},
			`{"type":"exit","terminal_id":"t1","signal":"SIGKILL"}`,
		},
		{
			"error",
			DaemonTerminalMessage{Type: "error", RequestID: "r1", Code: "path_not_allowed", Message: "outside"},
			`{"type":"error","request_id":"r1","code":"path_not_allowed","message":"outside"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("marshal\n got %s\nwant %s", got, tc.want)
			}
			var back DaemonTerminalMessage
			if err := json.Unmarshal([]byte(tc.want), &back); err != nil {
				t.Fatal(err)
			}
			if back.Type != tc.in.Type || back.RequestID != tc.in.RequestID || back.TerminalID != tc.in.TerminalID ||
				back.Cwd != tc.in.Cwd || back.Cols != tc.in.Cols || back.Rows != tc.in.Rows || back.PID != tc.in.PID ||
				back.Signal != tc.in.Signal || back.Code != tc.in.Code || back.Message != tc.in.Message {
				t.Fatalf("round trip lost fields: %+v", back)
			}
			switch {
			case tc.in.ExitCode == nil && back.ExitCode != nil:
				t.Fatalf("exit_code decoded as %d, want absent", *back.ExitCode)
			case tc.in.ExitCode != nil && (back.ExitCode == nil || *back.ExitCode != *tc.in.ExitCode):
				t.Fatalf("exit_code decoded as %v, want %d", back.ExitCode, *tc.in.ExitCode)
			}
		})
	}
}

// The browser contract spells exitCode out in both cases: a number, or null
// when a signal ended the process.
func TestBrowserTerminalExitWireShape(t *testing.T) {
	zero := 0
	byCode, err := json.Marshal(TerminalExit{Type: TerminalMsgExit, TerminalID: "t1", ExitCode: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if string(byCode) != `{"type":"exit","terminalId":"t1","exitCode":0}` {
		t.Fatalf("exit by code: %s", byCode)
	}
	bySignal, err := json.Marshal(TerminalExit{Type: TerminalMsgExit, TerminalID: "t1", Signal: "SIGTERM"})
	if err != nil {
		t.Fatal(err)
	}
	if string(bySignal) != `{"type":"exit","terminalId":"t1","exitCode":null,"signal":"SIGTERM"}` {
		t.Fatalf("exit by signal: %s", bySignal)
	}

	ready, err := json.Marshal(TerminalReady{Type: TerminalMsgReady, TerminalID: "t1", PID: 7, Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	if string(ready) != `{"type":"ready","terminalId":"t1","pid":7,"cols":120,"rows":40}` {
		t.Fatalf("ready: %s", ready)
	}

	errMsg, err := json.Marshal(TerminalError{Type: TerminalMsgError, Code: TerminalErrPathNotAllowed, Message: "outside"})
	if err != nil {
		t.Fatal(err)
	}
	if string(errMsg) != `{"type":"error","code":"path_not_allowed","message":"outside"}` {
		t.Fatalf("error: %s", errMsg)
	}
}

func TestDecodeBrowserTerminalMessage(t *testing.T) {
	var msg BrowserTerminalMessage
	if err := json.Unmarshal([]byte(`{"type":"open","cwd":"/srv/app","cols":120,"rows":40,"daemonId":"d1"}`), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != TerminalMsgOpen || msg.Cwd != "/srv/app" || msg.Cols != 120 || msg.Rows != 40 || msg.DaemonID != "d1" {
		t.Fatalf("open decoded as %+v", msg)
	}
	msg = BrowserTerminalMessage{}
	if err := json.Unmarshal([]byte(`{"type":"close","terminalId":"t1"}`), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != TerminalMsgClose || msg.TerminalID != "t1" {
		t.Fatalf("close decoded as %+v", msg)
	}
	if err := json.Unmarshal([]byte(`[1,2]`), &msg); err == nil {
		t.Fatal("a JSON array decoded as a control message")
	}
}

func TestValidTerminalSize(t *testing.T) {
	cases := []struct {
		cols, rows int
		want       bool
	}{
		{1, 1, true},
		{MaxTerminalCols, MaxTerminalRows, true},
		{0, 24, false},
		{80, 0, false},
		{-1, 24, false},
		{MaxTerminalCols + 1, 24, false},
		{80, MaxTerminalRows + 1, false},
	}
	for _, tc := range cases {
		if got := ValidTerminalSize(tc.cols, tc.rows); got != tc.want {
			t.Fatalf("ValidTerminalSize(%d, %d) = %v, want %v", tc.cols, tc.rows, got, tc.want)
		}
	}
}

func TestKnownTerminalError(t *testing.T) {
	for _, code := range []string{TerminalErrFlagDisabled, TerminalErrBadRequest, TerminalErrTerminalRootUnconfigured,
		TerminalErrPathNotAllowed, TerminalErrDaemonUnavailable, TerminalErrNotFound,
		TerminalErrUnsupportedPlatform, TerminalErrInternal,
		TerminalErrTreeNotReady, TerminalErrRuntimeOffline, TerminalErrProjectNotFound} {
		if !KnownTerminalError(code) {
			t.Fatalf("%q should be a known code", code)
		}
	}
	if KnownTerminalError("daemon:gone") || KnownTerminalError("") {
		t.Fatal("an unknown code read as known")
	}
}

func TestTerminalAgentOpenWireShape(t *testing.T) {
	// What the browser sends, as the relay decodes it.
	var open BrowserTerminalMessage
	if err := json.Unmarshal([]byte(`{"type":"open","cols":120,"rows":40,"agent":{"issue_id":"i-1","tree_id":"t-1"}}`), &open); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if open.Type != TerminalMsgOpen || open.Cwd != "" || open.Agent == nil || open.Agent.IssueID != "i-1" || open.Agent.TreeID != "t-1" {
		t.Fatalf("decoded %+v", open)
	}
	// A shell open carries no agent key at all.
	plain, err := json.Marshal(BrowserTerminalMessage{Type: TerminalMsgOpen, Cwd: "/srv", Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "agent") {
		t.Fatalf("a shell open mentions agent: %s", plain)
	}

	// What the server sends the daemon, pinned like the other daemon frames.
	got, err := json.Marshal(DaemonTerminalMessage{
		Type: TerminalMsgOpen, RequestID: "r1", Cwd: "/srv/tree", Cols: 120, Rows: 40,
		Agent: &DaemonTerminalAgent{IssueID: "i-1", StartTreeID: "t-1", OtherDirs: []string{"/srv/other"}, Brief: "MUL-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"open","request_id":"r1","cwd":"/srv/tree","cols":120,"rows":40,` +
		`"agent":{"issue_id":"i-1","start_tree_id":"t-1","other_dirs":["/srv/other"],"brief":"MUL-1"}}`
	if string(got) != want {
		t.Fatalf("agent open = %s, want %s", got, want)
	}
	var back DaemonTerminalMessage
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.Agent == nil || back.Agent.StartTreeID != "t-1" || len(back.Agent.OtherDirs) != 1 || back.Agent.Brief != "MUL-1" {
		t.Fatalf("decoded %+v", back)
	}
}

func TestTerminalProjectOpenWireShape(t *testing.T) {
	// What the browser sends, as the relay decodes it: the default project by
	// name, a registered folder by the id of the resource that holds it.
	for in, want := range map[string]string{
		`{"type":"open","project":"default","cols":120,"rows":40}`:         TerminalProjectDefault,
		`{"type":"open","project":"6d1f0e3a-0000-4000-8000-000000000001"}`: "6d1f0e3a-0000-4000-8000-000000000001",
	} {
		var open BrowserTerminalMessage
		if err := json.Unmarshal([]byte(in), &open); err != nil || open.Project != want || open.Cwd != "" || open.Agent != nil {
			t.Fatalf("decode %s: %v, %+v", in, err, open)
		}
	}
	// An open for a directory or an agent mentions no project key.
	for _, plain := range []BrowserTerminalMessage{
		{Type: TerminalMsgOpen, Cwd: "/srv", Cols: 120, Rows: 40},
		{Type: TerminalMsgOpen, Cols: 120, Rows: 40, Agent: &BrowserAgentRef{IssueID: "i-1", TreeID: "t-1"}},
	} {
		got, err := json.Marshal(plain)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(got), "project") {
			t.Fatalf("%+v mentions project: %s", plain, got)
		}
	}

	// What the server sends the daemon, pinned like the other daemon frames: a
	// registered folder with its path, the default project without one.
	for open, want := range map[DaemonTerminalMessage]string{
		{Type: TerminalMsgOpen, RequestID: "r1", Cwd: "/home/me/code/app", Cols: 120, Rows: 40, Project: DaemonTerminalProjectFolder}: `{"type":"open","request_id":"r1","cwd":"/home/me/code/app","cols":120,"rows":40,"project":"folder"}`,
		{Type: TerminalMsgOpen, RequestID: "r2", Cols: 120, Rows: 40, Project: DaemonTerminalProjectDefault}:                          `{"type":"open","request_id":"r2","cols":120,"rows":40,"project":"default"}`,
	} {
		got, err := json.Marshal(open)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("project open = %s, want %s", got, want)
		}
		var back DaemonTerminalMessage
		if err := json.Unmarshal(got, &back); err != nil || back.Project != open.Project || back.Cwd != open.Cwd {
			t.Fatalf("decode %s: %v, %+v", got, err, back)
		}
	}
}

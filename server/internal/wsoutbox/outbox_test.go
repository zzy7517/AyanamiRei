package wsoutbox

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestKeepsRoomForControlFrames(t *testing.T) {
	o := New(6, 2, 1<<20)
	for i := 0; i < 4; i++ {
		if !o.TrySend(Text([]byte{byte(i)}), false) {
			t.Fatalf("stream frame %d refused with room to spare", i)
		}
	}
	if o.TrySend(Text([]byte("x")), false) {
		t.Fatal("a stream frame took a reserved slot")
	}
	if !o.TrySend(Text([]byte("error")), true) {
		t.Fatal("the reserve refused a control frame")
	}
	if o.WaitRoom(time.Now().Add(time.Second), nil) {
		t.Fatal("WaitRoom reports room while the stream slots are full")
	}
	if got := o.Take(); string(got.Data) != "\x00" {
		t.Fatalf("frames are not FIFO: %q", got.Data)
	}
	// Four stream frames filled the stream slots; two must leave before one
	// fits again.
	if o.WaitRoom(time.Now().Add(time.Second), nil) {
		t.Fatal("WaitRoom reports room while the stream slots are full")
	}
	o.Take()
	if !o.WaitRoom(time.Now().Add(time.Second), nil) {
		t.Fatal("WaitRoom did not see the freed slot")
	}
	o.Take()
	o.Take()
	got, ok := o.TryTake()
	if !ok || string(got.Data) != "error" {
		t.Fatalf("next frame = %v %q, want the control frame after the stream frames that preceded it", ok, got.Data)
	}
	if o.bytes.Load() != 0 {
		t.Fatalf("byte accounting left %d after draining", o.bytes.Load())
	}
}

func TestByteCap(t *testing.T) {
	o := New(100, 0, 10)
	if !o.TrySend(Text(make([]byte, 6)), false) {
		t.Fatal("6 bytes should fit under a 10-byte cap")
	}
	if o.TrySend(Text(make([]byte, 5)), false) {
		t.Fatal("a frame that crosses the byte cap was accepted")
	}
	if o.bytes.Load() != 6 {
		t.Fatalf("a refused frame changed the byte count: %d", o.bytes.Load())
	}
	if !o.TrySend(Text(make([]byte, 64)), true) {
		t.Fatal("the byte cap refused a control frame")
	}
}

func TestFrameTypes(t *testing.T) {
	if got := Text(nil); got.Type != websocket.TextMessage {
		t.Fatalf("Text() type = %d, want %d", got.Type, websocket.TextMessage)
	}
	if got := Binary(nil); got.Type != websocket.BinaryMessage {
		t.Fatalf("Binary() type = %d, want %d", got.Type, websocket.BinaryMessage)
	}
}

func TestWaitRoomStops(t *testing.T) {
	o := New(2, 1, 1<<20)
	o.TrySend(Binary([]byte("x")), false)
	done := make(chan struct{})
	close(done)
	if o.WaitRoom(time.Now().Add(time.Hour), done) {
		t.Fatal("WaitRoom reported room after the connection ended")
	}
	if o.WaitRoom(time.Now().Add(10*time.Millisecond), nil) {
		t.Fatal("WaitRoom reported room after the deadline")
	}
}

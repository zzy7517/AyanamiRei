// Package wsoutbox is the bounded outbound queue of a WebSocket connection,
// shared by the terminal sockets on both the server and the daemon.
//
// One writer goroutine (WritePump) drains a queue that every other goroutine
// fills without blocking. The queue is bounded in frames and in bytes, and
// keeps a few slots for control frames, so a peer that stops reading costs a
// fixed amount of memory and still receives the message that ends its
// connection.
package wsoutbox

import (
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Liveness of a connection served by WritePump: a write must finish within
// WriteWait, and a peer must answer a ping within PongWait.
const (
	WriteWait  = 10 * time.Second
	PongWait   = 60 * time.Second
	PingPeriod = (PongWait * 9) / 10
)

// Frame is one WebSocket message on a queue. A frame may be sent to several
// connections, so nothing may modify it once it is queued.
type Frame struct {
	Type int
	Data []byte
}

// Text builds a control frame.
func Text(data []byte) Frame { return Frame{Type: websocket.TextMessage, Data: data} }

// Binary builds a stream frame.
func Binary(data []byte) Frame { return Frame{Type: websocket.BinaryMessage, Data: data} }

// Outbox is the queue one connection's writer drains. Stream frames are
// refused once the queue holds maxFrames-reserve frames or maxBytes,
// whichever comes first; control frames may use the reserve and are not held
// to the byte cap. What a refusal means for the connection is the caller's
// decision.
//
// All methods are safe for concurrent use.
type Outbox struct {
	ch       chan Frame
	reserve  int
	maxBytes int64
	bytes    atomic.Int64
	// room is signalled whenever a frame leaves, waking a sender waiting in
	// WaitRoom.
	room chan struct{}
}

// New returns an outbox holding at most maxFrames frames.
func New(maxFrames, reserve int, maxBytes int64) *Outbox {
	return &Outbox{
		ch:       make(chan Frame, maxFrames),
		reserve:  reserve,
		maxBytes: maxBytes,
		room:     make(chan struct{}, 1),
	}
}

// TrySend appends one frame without blocking and reports whether it fit.
func (o *Outbox) TrySend(f Frame, control bool) bool {
	size := int64(len(f.Data))
	total := o.bytes.Add(size)
	if !control && (len(o.ch) >= cap(o.ch)-o.reserve || total > o.maxBytes) {
		o.bytes.Add(-size)
		return false
	}
	select {
	case o.ch <- f:
		return true
	default:
		o.bytes.Add(-size)
		return false
	}
}

// Take removes the oldest frame, blocking until there is one. Only the
// connection's writer calls it.
func (o *Outbox) Take() Frame {
	f := <-o.ch
	o.release(f)
	return f
}

// TryTake is Take without blocking.
func (o *Outbox) TryTake() (Frame, bool) {
	select {
	case f := <-o.ch:
		o.release(f)
		return f, true
	default:
		return Frame{}, false
	}
}

func (o *Outbox) release(f Frame) {
	o.bytes.Add(-int64(len(f.Data)))
	select {
	case o.room <- struct{}{}:
	default:
	}
}

// HasRoom reports whether a stream frame fits now.
func (o *Outbox) HasRoom() bool {
	return len(o.ch) < cap(o.ch)-o.reserve && o.bytes.Load() < o.maxBytes
}

// WaitRoom blocks until a stream frame fits, the deadline passes, or stop
// closes. It returns true only when a frame fits.
func (o *Outbox) WaitRoom(deadline time.Time, stop <-chan struct{}) bool {
	if o.HasRoom() {
		return true
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-o.room:
			if o.HasRoom() {
				return true
			}
		case <-timer.C:
			return o.HasRoom()
		case <-stop:
			return false
		}
	}
}

// PrepareRead limits one inbound message to limit bytes and starts the
// pong-driven read deadline that WritePump's pings keep extending.
func PrepareRead(conn *websocket.Conn, limit int64) error {
	conn.SetReadLimit(limit)
	if err := conn.SetReadDeadline(time.Now().Add(PongWait)); err != nil {
		return err
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(PongWait))
	})
	return nil
}

// WritePump is the one writer of a connection. It writes queued frames and
// keeps the peer alive with pings. Once done closes it writes what is queued
// at that point, sends a close message and closes the connection. A write
// error, or a write that misses WriteWait, ends the pump the same way, which
// is what unblocks the connection's reader.
func WritePump(conn *websocket.Conn, out *Outbox, done <-chan struct{}) {
	ticker := time.NewTicker(PingPeriod)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()
	write := func(typ int, data []byte) bool {
		conn.SetWriteDeadline(time.Now().Add(WriteWait))
		return conn.WriteMessage(typ, data) == nil
	}
	for {
		select {
		case f := <-out.ch:
			out.release(f)
			if !write(f.Type, f.Data) {
				return
			}
		case <-ticker.C:
			if !write(websocket.PingMessage, nil) {
				return
			}
		case <-done:
			for {
				f, ok := out.TryTake()
				if !ok {
					conn.WriteControl(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
						time.Now().Add(WriteWait))
					return
				}
				if !write(f.Type, f.Data) {
					return
				}
			}
		}
	}
}

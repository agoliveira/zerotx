package ipc

import (
	"testing"
)

// TestDispatch_HeartbeatRxInvokesCallback: a MsgHeartbeat frame
// should invoke OnHeartbeatRx with the sequence number from the
// payload's first byte.
func TestDispatch_HeartbeatRxInvokesCallback(t *testing.T) {
	var gotSeq byte
	var fired bool
	l := &Link{
		OnHeartbeatRx: func(seq byte) {
			gotSeq = seq
			fired = true
		},
	}
	l.dispatch(Frame{
		Type:    MsgHeartbeat,
		Payload: []byte{0x42, 0x00, 0x00},
	})
	if !fired {
		t.Fatalf("OnHeartbeatRx not invoked")
	}
	if gotSeq != 0x42 {
		t.Errorf("seq: got 0x%02x, want 0x42", gotSeq)
	}
}

// TestDispatch_HeartbeatRxNilCallbackIsSafe: an unset OnHeartbeatRx
// must not panic on receipt of a heartbeat frame. The use of nil
// is the common case during boot before main() wires the callback,
// and SITL mode never sets it at all.
func TestDispatch_HeartbeatRxNilCallbackIsSafe(t *testing.T) {
	l := &Link{} // OnHeartbeatRx is nil
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("dispatch panicked on nil OnHeartbeatRx: %v", r)
		}
	}()
	l.dispatch(Frame{Type: MsgHeartbeat, Payload: []byte{1}})
}

// TestDispatch_HeartbeatRxEmptyPayloadIsSafe: a heartbeat frame
// with empty payload should still invoke the callback (with seq=0)
// rather than panic on out-of-range payload access. This guards
// against firmware that for whatever reason emits a zero-byte
// heartbeat.
func TestDispatch_HeartbeatRxEmptyPayloadIsSafe(t *testing.T) {
	var fired bool
	l := &Link{
		OnHeartbeatRx: func(seq byte) {
			fired = true
		},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("dispatch panicked on empty payload: %v", r)
		}
	}()
	l.dispatch(Frame{Type: MsgHeartbeat, Payload: nil})
	if !fired {
		t.Errorf("OnHeartbeatRx should fire even with empty payload")
	}
}

// TestDispatch_HeartbeatRxDoesNotFallthroughToOnFrame: heartbeat
// frames must NOT also be delivered to the generic OnFrame callback.
// Otherwise consumers that registered OnFrame to catch unknown
// frame types would see heartbeats as "unknown", which they aren't.
func TestDispatch_HeartbeatRxDoesNotFallthroughToOnFrame(t *testing.T) {
	var onFrameCalled bool
	l := &Link{
		OnHeartbeatRx: func(seq byte) {},
		OnFrame:       func(f Frame) { onFrameCalled = true },
	}
	l.dispatch(Frame{Type: MsgHeartbeat, Payload: []byte{1}})
	if onFrameCalled {
		t.Errorf("MsgHeartbeat fell through to OnFrame; should be consumed by OnHeartbeatRx case")
	}
}

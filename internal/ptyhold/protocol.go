package ptyhold

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Protocol is bumped on incompatible wire changes. An application that meets a
// holder speaking another one shuts it down and starts its own: the sessions
// in it are lost, which beats two programs disagreeing about the bytes.
const Protocol = 2

// Frame types. Control frames carry JSON, data frames raw bytes.
const (
	tCtrl byte = 1 // application → holder: a request
	tResp byte = 2 // holder → application: the answer to one
	tEvt  byte = 3 // holder → application: something that happened
	tOut  byte = 4 // holder → application: what a session printed
	tIn   byte = 5 // application → holder: what a session is typed
)

// maxFrame guards against a corrupt length prefix.
const maxFrame = 32 << 20

// frame is one message on the wire: uint32 payload length, uint8 type, uint8
// id length, the id, the payload.
//
// The id is the application's own name for the terminal rather than a number
// the holder hands out: output of a session just started may arrive before the
// answer saying it started, and the application has to know whose it is.
type frame struct {
	typ     byte
	id      string
	payload []byte
}

func writeFrame(w io.Writer, f frame) error {
	if len(f.id) > 255 {
		return errors.New("session id too long")
	}
	buf := make([]byte, 6+len(f.id)+len(f.payload))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(f.payload)))
	buf[4] = f.typ
	buf[5] = byte(len(f.id))
	copy(buf[6:], f.id)
	copy(buf[6+len(f.id):], f.payload)
	// One write per frame: two writers interleaving halves of frames is the
	// failure a mutex around this call is there to prevent, and one write
	// keeps that true even for a caller that forgets it.
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) (frame, error) {
	head := make([]byte, 6)
	if _, err := io.ReadFull(r, head); err != nil {
		return frame{}, err
	}
	length := binary.BigEndian.Uint32(head[0:4])
	if length > maxFrame {
		return frame{}, fmt.Errorf("frame too large: %d", length)
	}
	rest := make([]byte, int(head[5])+int(length))
	if _, err := io.ReadFull(r, rest); err != nil {
		return frame{}, err
	}
	return frame{typ: head[4], id: string(rest[:head[5]]), payload: rest[head[5]:]}, nil
}

// Kinds of session, by who owns it: a screen's shell belongs to the person, a
// run's CLI to the stage it works.
const (
	KindScreen = "screen"
	KindRun    = "run"
)

// Label is what the application needs to know whose a session is after it has
// been started again and remembers nothing. The holder only stores it.
type Label struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Card    string `json:"card,omitempty"`
	Screen  string `json:"screen,omitempty"`
	Command string `json:"command,omitempty"`
}

// Info is one session as the holder lists it.
type Info struct {
	Label   Label `json:"label"`
	Running bool  `json:"running"`
	// The size its screen is drawn for: a screen taken back has to be kept at
	// that size, or what arrives next lands in the wrong places.
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// request is the payload of a tCtrl frame. Req pairs it with its answer.
type request struct {
	Req int64  `json:"req"`
	Op  string `json:"op"` // hello start attach resize hangup kill forget list shutdown

	Protocol int    `json:"protocol,omitempty"`
	ID       string `json:"id,omitempty"`
	Label    Label  `json:"label,omitzero"`
	Spec     Spec   `json:"spec,omitzero"`
	Cols     int    `json:"cols,omitempty"`
	Rows     int    `json:"rows,omitempty"`
}

// response is the payload of a tResp frame.
type response struct {
	Req int64  `json:"req"`
	Err string `json:"err,omitempty"`

	Protocol int    `json:"protocol,omitempty"`
	Pid      int    `json:"pid,omitempty"`
	Running  bool   `json:"running,omitempty"`
	Sessions []Info `json:"sessions,omitempty"`
}

// event is the payload of a tEvt frame.
type event struct {
	Event string `json:"event"` // exited
	ID    string `json:"id"`
}

func marshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

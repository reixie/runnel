package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
)

const (
	protocolVersion = 4
	// GUI ownership is independent of the terminal wire protocol. Stage 2's
	// role-aware development controller already uses version 2 here.
	guiControlVersion = 2
	maxPacket         = 16 << 20
	maxInput          = 32 << 10
	ioTimeout         = 3 * time.Second
	heartbeat         = 15 * time.Second
)

// Every packet has a big-endian uint32 JSON byte length. Only hello/welcome
// negotiate versions; no session is touched until negotiation succeeds.
type packet struct {
	ack        chan struct{}   // local writer completion; never serialized
	Type       string          `json:"type"`
	Version    int             `json:"version,omitempty"`
	Error      string          `json:"error,omitempty"`
	Seq        uint64          `json:"seq,omitempty"`
	Data       []byte          `json:"data,omitempty"`
	Cols       int             `json:"cols,omitempty"`
	Rows       int             `json:"rows,omitempty"`
	CellW      int             `json:"cell_w,omitempty"`
	CellH      int             `json:"cell_h,omitempty"`
	Theme      *terminal.Theme `json:"theme,omitempty"`
	Exited     bool            `json:"exited,omitempty"`
	Code       int             `json:"code,omitempty"`
	PID        int             `json:"pid,omitempty"`
	Role       string          `json:"role,omitempty"`
	Owner      string          `json:"owner,omitempty"`
	Generation uint64          `json:"generation,omitempty"`
	Request    uint64          `json:"request,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	PaneID     string          `json:"pane_id,omitempty"`
	NodeID     string          `json:"node_id,omitempty"`
	Axis       string          `json:"axis,omitempty"`
	Ratio      float64         `json:"ratio,omitempty"`
	Revision   uint64          `json:"revision,omitempty"`
	ProjectID  string          `json:"project_id,omitempty"`
	Path       string          `json:"path,omitempty"`
	Name       string          `json:"name,omitempty"`
	Catalog    *catalog        `json:"catalog,omitempty"`
}

func readPacket(r io.Reader) (packet, error) { return readPacketLimit(r, maxPacket) }
func readPacketLimit(r io.Reader, limit uint32) (packet, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return packet{}, err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > limit {
		return packet{}, fmt.Errorf("invalid packet length %d", n)
	}
	b := make([]byte, int(n))
	if _, err := io.ReadFull(r, b); err != nil {
		return packet{}, err
	}
	var p packet
	err := json.Unmarshal(b, &p)
	return p, err
}

func writePacket(w io.Writer, p packet) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(b) > maxPacket {
		return fmt.Errorf("packet exceeds %d bytes", maxPacket)
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, err = w.Write(h[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func (p packet) validSize() bool {
	return p.Cols >= 2 && p.Cols <= 500 && p.Rows >= 2 && p.Rows <= 300 &&
		p.CellW >= 1 && p.CellW <= 128 && p.CellH >= 1 && p.CellH <= 128
}

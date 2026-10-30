package proto

import "github.com/atmarx/zawiper/internal/model"

// The link: one TLS connection a bay dials out to the hub and keeps open,
// carrying newline-delimited JSON Frames both ways.  The hub knows which bay
// it's talking to from the client certificate (CN bay/<host>/<name>), never
// from anything the bay says; a Hello that disagrees with the certificate
// ends the connection.
//
//	bay → hub: hello, status (whenever the snapshot changes, and every 10 s
//	           as a heartbeat), record (a finished record from the spool),
//	           reply (to a command)
//	hub → bay: command, stored (a record is safe on the hub; the bay may
//	           drop it from its spool)
const (
	FrameHello   = "hello"
	FrameStatus  = "status"
	FrameRecord  = "record"
	FrameReply   = "reply"
	FrameCommand = "command"
	FrameStored  = "stored"
)

// LinkPort is where the hub listens for bays.
const LinkPort = 8461

type Frame struct {
	Type string `json:"type"`
	// ID pairs a command with its reply, and a record with its stored.
	ID string `json:"id,omitempty"`

	Hello    *Hello          `json:"hello,omitempty"`
	Snapshot *model.Snapshot `json:"snapshot,omitempty"`
	Request  *Request        `json:"request,omitempty"`
	Reply    *Reply          `json:"reply,omitempty"`
	Record   *RecordUpload   `json:"record,omitempty"`
}

type Hello struct {
	Host    string   `json:"host"`
	Bay     string   `json:"bay"`
	Version string   `json:"version"`
	Caps    []string `json:"caps"` // wipe, load, sas
}

// RecordUpload is one record folder from a bay's spool.  Files holds only
// names in records.Files; the hub refuses anything else.
type RecordUpload struct {
	Serial string            `json:"serial"`
	ID     string            `json:"id"`
	Files  map[string][]byte `json:"files"`
}

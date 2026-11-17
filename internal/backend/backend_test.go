package backend

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"modembridge/internal/config"
)

func TestEnrichedSession(t *testing.T) {
	e, err := New(config.Route{Protocol: "jatena", Mode: "listen", Address: "127.0.0.1:0", Transport: "enriched"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	peer, err := net.Dial("tcp", e.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.SetDeadline(time.Now().Add(time.Second))
	s, err := e.Attach(ctx, map[string]any{"version": 1, "protocol": "jatena"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	read := func() (byte, []byte) {
		t.Helper()
		var header [5]byte
		if _, err := io.ReadFull(peer, header[:]); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, binary.BigEndian.Uint32(header[1:]))
		if _, err := io.ReadFull(peer, data); err != nil {
			t.Fatal(err)
		}
		return header[0], data
	}
	typeID, metadata := read()
	var meta map[string]any
	if typeID != Start || json.Unmarshal(metadata, &meta) != nil || meta["protocol"] != "jatena" {
		t.Fatal("invalid session start")
	}
	// Fragment both frame header and binary payload across writes.
	writeDone := make(chan error, 1)
	go func() {
		for _, b := range []byte{Data, 0, 0, 0, 3, 0, 0xff, 3} {
			if _, err := peer.Write([]byte{b}); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()
	data, err := s.Receive()
	if err != nil || string(data) != string([]byte{0, 0xff, 3}) {
		t.Fatalf("binary data: %x %v", data, err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err = s.Send(data); err != nil {
		t.Fatal(err)
	}
	typeID, data = read()
	if typeID != Data || len(data) != 3 {
		t.Fatal("invalid data frame")
	}
	peer.Write([]byte{Hangup, 0, 0, 0, 0})
	if _, err = s.Receive(); err == nil {
		t.Fatal("hangup ignored")
	}
	s.End("backend requested hangup")
	typeID, _ = read()
	if typeID != End {
		t.Fatal("missing session end")
	}
}

func TestRejectOversizedFrameAndAttachTimeout(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	s := &Session{Conn: local, enriched: true}
	go func() {
		var header [5]byte
		header[0] = Data
		binary.BigEndian.PutUint32(header[1:], MaxFrame+1)
		peer.Write(header[:])
	}()
	if _, err := s.Receive(); err == nil {
		t.Fatal("oversized frame accepted")
	}
	e, err := New(config.Route{Mode: "listen", Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err = e.Attach(ctx, nil); err == nil {
		t.Fatal("attach timeout ignored")
	}
}

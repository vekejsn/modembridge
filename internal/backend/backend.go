package backend

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"modembridge/internal/config"
	"modembridge/internal/modem"
)

const MaxFrame = 65536
const (
	Start  byte = 1
	Data   byte = 2
	End    byte = 3
	Hangup byte = 4
)

type Endpoint struct {
	Route    config.Route
	listener *net.TCPListener
}

func New(r config.Route) (*Endpoint, error) {
	e := &Endpoint{Route: r}
	if r.Mode == "listen" {
		addr, err := net.ResolveTCPAddr("tcp", r.Address)
		if err != nil {
			return nil, err
		}
		e.listener, err = net.ListenTCP("tcp", addr)
		if err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *Endpoint) Close() {
	if e.listener != nil {
		e.listener.Close()
	}
}

func (e *Endpoint) Attach(ctx context.Context, metadata any) (*Session, error) {
	var conn net.Conn
	var err error
	if e.listener == nil {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", e.Route.Address)
	} else {
		for {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			e.listener.SetDeadline(time.Now().Add(200 * time.Millisecond))
			conn, err = e.listener.Accept()
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			break
		}
	}
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		tcp.SetNoDelay(true)
	}
	s := &Session{Conn: conn, enriched: e.Route.Transport == "enriched"}
	if s.enriched {
		payload, err := json.Marshal(metadata)
		if err == nil {
			err = s.writeFrame(Start, payload)
		}
		if err != nil {
			conn.Close()
			return nil, err
		}
	}
	return s, nil
}

type Session struct {
	net.Conn
	enriched bool
}

func (s *Session) Receive() ([]byte, error) {
	if !s.enriched {
		buf := make([]byte, 4096)
		n, err := s.Conn.Read(buf)
		if n > 0 {
			return buf[:n], nil
		}
		return nil, err
	}
	var header [5]byte
	if _, err := io.ReadFull(s.Conn, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[1:])
	if n > MaxFrame {
		return nil, errors.New("backend frame exceeds limit")
	}
	if header[0] != Data && header[0] != Hangup {
		return nil, fmt.Errorf("unexpected backend frame type %d", header[0])
	}
	if header[0] == Hangup && n != 0 {
		return nil, errors.New("hangup payload must be empty")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(s.Conn, buf); err != nil {
		return nil, err
	}
	if header[0] == Hangup {
		return nil, errors.New("backend requested hangup")
	}
	if n == 0 {
		return nil, errors.New("empty data frame")
	}
	return buf, nil
}

func (s *Session) Send(data []byte) error {
	if s.enriched {
		return s.writeFrame(Data, data)
	}
	s.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return modem.WriteAll(s.Conn, data)
}

func (s *Session) End(reason string) {
	if s.enriched {
		payload, _ := json.Marshal(map[string]string{"reason": reason})
		_ = s.writeFrame(End, payload)
	}
}

func (s *Session) writeFrame(kind byte, payload []byte) error {
	if len(payload) > MaxFrame {
		return errors.New("frame exceeds limit")
	}
	s.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if err := modem.WriteAll(s.Conn, header[:]); err != nil {
		return err
	}
	return modem.WriteAll(s.Conn, payload)
}

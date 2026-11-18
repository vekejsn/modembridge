package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.bug.st/serial"
	"modembridge/internal/config"
	"modembridge/internal/modem"
	"modembridge/internal/protocol"
)

// A serial-port simulator, including timed reads and DTR, connected to an
// independent AT-speaking peer. The production lifecycle runs unmodified.
type testPort struct {
	net.Conn
	timeout time.Duration
	dtr     atomic.Bool
}

func (p *testPort) SetDTR(value bool) error                  { p.dtr.Store(value); return nil }
func (p *testPort) SetReadTimeout(value time.Duration) error { p.timeout = value; return nil }
func (p *testPort) GetModemStatusBits() (*serial.ModemStatusBits, error) {
	return &serial.ModemStatusBits{DCD: p.dtr.Load()}, nil
}
func (p *testPort) Read(b []byte) (int, error) {
	p.Conn.SetReadDeadline(time.Now().Add(p.timeout))
	n, err := p.Conn.Read(b)
	if e, ok := err.(net.Error); ok && e.Timeout() {
		return n, nil
	}
	return n, err
}

func TestReconnectAfterModemDisappears(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c := config.Config{
		Modem: config.Modem{Device: "stable-device-id", Baud: 19200, AnswerAfterRings: 1, NegotiationSeconds: 2},
		Routes: []config.Route{{Protocol: "millennium", Mode: "connect", Address: "127.0.0.1:27276", Transport: "raw"}},
		IdentificationSeconds: 2, MaxIdentificationBytes: 1024, BackendSeconds: 2, IdleSeconds: 3, MaxSessionSeconds: 6, RecoverySeconds: 1,
	}
	var attempts atomic.Int32
	reconnected := make(chan struct{})
	s := Service{Config: c, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Open = func(config.Modem) (*modem.Modem, error) {
		switch attempts.Add(1) {
		case 1:
			local, peer := net.Pipe()
			go func() {
				defer peer.Close()
				r := bufio.NewReader(peer)
				for i := 0; i < 2; i++ {
					if _, err := r.ReadString('\r'); err != nil {
						return
					}
					if _, err := peer.Write([]byte("OK\r\n")); err != nil {
						return
					}
				}
				// The USB device disappears while waiting for a call.
			}()
			return modem.New(&testPort{Conn: local}, false)
		case 2:
			return nil, errors.New("device not present")
		default:
			local, peer := net.Pipe()
			go func() {
				defer peer.Close()
				r := bufio.NewReader(peer)
				for i := 0; i < 2; i++ {
					if _, err := r.ReadString('\r'); err != nil {
						return
					}
					if _, err := peer.Write([]byte("OK\r\n")); err != nil {
						return
					}
				}
				close(reconnected)
				<-ctx.Done()
			}()
			return modem.New(&testPort{Conn: local}, false)
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case <-reconnected:
		cancel()
	case <-ctx.Done():
		t.Fatal("service did not retry after the modem disappeared")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTwoCallsWithDifferentBackends(t *testing.T) {
	mm := []byte{2, 0, 6, 0x24}
	crc := protocol.CRC16(mm)
	mm = append(mm, byte(crc), byte(crc>>8), 3)
	j := []byte{1, 5, 0x70, 0x20, 0x95, 0x81, 3}
	frames := [][]byte{mm, j}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan error, 8)
	routes := make([]config.Route, 0, 2)
	for i, name := range []string{"millennium", "jatena"} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		routes = append(routes, config.Route{Protocol: name, Mode: "connect", Address: ln.Addr().String(), Transport: "raw"})
		go func(frame []byte) {
			peer, err := ln.Accept()
			if err != nil {
				results <- err
				return
			}
			defer peer.Close()
			peer.SetDeadline(time.Now().Add(5 * time.Second))
			opening := make([]byte, len(frame)+2)
			_, err = io.ReadFull(peer, opening)
			if err == nil && !bytes.Equal(opening, append(append([]byte{}, frame...), 0xaa, 0xbb)) {
				err = fmt.Errorf("opening changed: %x", opening)
			}
			if err == nil {
				err = modem.WriteAll(peer, []byte("\x00NO CARRIER\xff"))
			}
			response := make([]byte, 3)
			if err == nil {
				_, err = io.ReadFull(peer, response)
			}
			if err == nil && !bytes.Equal(response, []byte{0xfe, 0, 0xfd}) {
				err = fmt.Errorf("phone response changed: %x", response)
			}
			results <- err
		}(frames[i])
	}
	c := config.Config{Modem: config.Modem{Device: "simulated", Baud: 19200, MonitorCarrier: true, AnswerAfterRings: 1, NegotiationSeconds: 2}, Routes: routes,
		IdentificationSeconds: 2, MaxIdentificationBytes: 1024, BackendSeconds: 2, IdleSeconds: 3, MaxSessionSeconds: 6, RecoverySeconds: 1}
	var count atomic.Int32
	var portsMu sync.Mutex
	var ports []*testPort
	s := Service{Config: c, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Open = func(config.Modem) (*modem.Modem, error) {
		index := int(count.Add(1)) - 1
		if index >= len(frames) {
			cancel()
			return nil, context.Canceled
		}
		local, peer := net.Pipe()
		p := &testPort{Conn: local}
		portsMu.Lock()
		ports = append(ports, p)
		portsMu.Unlock()
		go func() {
			defer peer.Close()
			peer.SetDeadline(time.Now().Add(6 * time.Second))
			r := bufio.NewReader(peer)
			for {
				line, err := r.ReadString('\r')
				if err != nil {
					results <- err
					return
				}
				if line == "ATA\r" {
					break
				}
				if strings.Contains(line, "S0=0") {
					_, err = peer.Write([]byte("OK\r\nRING\r\n"))
				} else {
					_, err = peer.Write([]byte("OK\r\n"))
				}
				if err != nil {
					results <- err
					return
				}
			}
			// CONNECT and the first bytes share a read; more bytes arrive later.
			_, err := peer.Write(append([]byte("CONNECT 1200\r\n"), frames[index][:2]...))
			if err == nil {
				_, err = peer.Write(append(append([]byte{}, frames[index][2:]...), 0xaa, 0xbb))
			}
			data := make([]byte, len("\x00NO CARRIER\xff"))
			if err == nil {
				_, err = io.ReadFull(r, data)
			}
			if err == nil && !bytes.Equal(data, []byte("\x00NO CARRIER\xff")) {
				err = fmt.Errorf("backend bytes changed: %x", data)
			}
			if err == nil {
				_, err = peer.Write([]byte{0xfe, 0, 0xfd})
			}
			results <- err
			// Keep carrier up until ModemBridge ends the session.
			_, _ = io.Copy(io.Discard, r)
		}()
		return modem.New(p, true)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	for range 4 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("simulated call timed out")
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not recover after calls")
	}
	portsMu.Lock()
	defer portsMu.Unlock()
	for _, p := range ports {
		if p.dtr.Load() {
			t.Fatal("DTR not dropped on hangup")
		}
	}
}

type sourceFunc func(context.Context) ([]byte, error)

func (f sourceFunc) Read(ctx context.Context) ([]byte, error) { return f(ctx) }

func TestIdentificationLimits(t *testing.T) {
	names := []string{"millennium", "jatena"}
	_, _, err := Identify(context.Background(), sourceFunc(func(context.Context) ([]byte, error) { return bytes.Repeat([]byte{0xff}, 8), nil }), names, 8)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("missing buffer limit: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _, err = Identify(ctx, sourceFunc(func(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }), names, 8)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing identification timeout: %v", err)
	}
}

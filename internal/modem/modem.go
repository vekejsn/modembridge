package modem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.bug.st/serial"
	"modembridge/internal/config"
)

var ErrCarrierLost = errors.New("carrier lost")

type Port interface {
	io.ReadWriteCloser
	SetDTR(bool) error
	SetReadTimeout(time.Duration) error
	GetModemStatusBits() (*serial.ModemStatusBits, error)
}

type event struct {
	data []byte
	err  error
}

type Modem struct {
	port      Port
	events    chan event
	done      chan struct{}
	pending   []byte
	skipLF    bool
	carrier   atomic.Bool
	monitor   bool
	closeOnce sync.Once
	closeErr  error
}

func Open(c config.Modem) (*Modem, error) {
	p, err := openPort(c)
	if err != nil {
		return nil, err
	}
	return New(p, c.MonitorCarrier)
}

func New(p Port, monitor bool) (*Modem, error) {
	if err := p.SetReadTimeout(200 * time.Millisecond); err != nil {
		p.Close()
		return nil, err
	}
	if err := p.SetDTR(true); err != nil {
		p.Close()
		return nil, fmt.Errorf("assert DTR: %w", err)
	}
	m := &Modem{port: p, events: make(chan event, 8), done: make(chan struct{}), monitor: monitor}
	go m.readLoop()
	return m, nil
}

func (m *Modem) readLoop() {
	defer close(m.events)
	send := func(e event) bool {
		select {
		case m.events <- e:
			return true
		case <-m.done:
			return false
		}
	}
	for {
		buf := make([]byte, 4096)
		n, err := m.port.Read(buf)
		if n > 0 && !send(event{data: buf[:n]}) {
			return
		}
		if err != nil {
			send(event{err: err})
			return
		}
		if m.monitor && m.carrier.Load() {
			bits, err := m.port.GetModemStatusBits()
			if err != nil {
				send(event{err: fmt.Errorf("read carrier: %w", err)})
				return
			}
			if !bits.DCD {
				send(event{err: ErrCarrierLost})
				return
			}
		}
		select {
		case <-m.done:
			return
		default:
		}
	}
}

// Close is called once, after the session's I/O workers have been cancelled.
// Dropping DTR is the required hangup mechanism for this implementation.
func (m *Modem) Close() error {
	m.closeOnce.Do(func() {
		m.carrier.Store(false)
		close(m.done)
		err := m.port.SetDTR(false)
		// Give the modem time to recognize the DTR transition before closing USB/TTY.
		time.Sleep(300 * time.Millisecond)
		closeErr := m.port.Close()
		if err != nil {
			m.closeErr = fmt.Errorf("hangup via DTR: %w", err)
		} else {
			m.closeErr = closeErr
		}
	})
	return m.closeErr
}

func (m *Modem) Write(data []byte) error { return WriteAll(m.port, data) }

func WriteAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (m *Modem) Read(ctx context.Context) ([]byte, error) {
	for {
		if len(m.pending) > 0 {
			data := m.pending
			m.pending = nil
			if m.skipLF {
				m.skipLF = false
				if data[0] == '\n' {
					data = data[1:]
				}
			}
			if len(data) > 0 {
				return data, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e, ok := <-m.events:
			if !ok {
				return nil, io.EOF
			}
			if e.err != nil {
				return nil, e.err
			}
			m.pending = e.data
		}
	}
}

func (m *Modem) line(ctx context.Context) (string, byte, error) {
	var line []byte
	for {
		data, err := m.Read(ctx)
		if err != nil {
			return "", 0, err
		}
		for i, b := range data {
			if b == '\r' || b == '\n' {
				m.pending = data[i+1:]
				if len(line) == 0 {
					break
				}
				return string(line), b, nil
			}
			line = append(line, b)
			if len(line) > 4096 {
				return "", 0, errors.New("modem result line too long")
			}
		}
	}
}

func (m *Modem) Command(ctx context.Context, command, expect string) (string, error) {
	if err := m.Write([]byte(command + "\r")); err != nil {
		return "", err
	}
	var lines []string
	for {
		line, _, err := m.line(ctx)
		if err != nil {
			return strings.Join(lines, "\n"), err
		}
		if line == command {
			continue
		}
		lines = append(lines, line)
		if line == expect {
			return strings.Join(lines, "\n"), nil
		}
		if line == "ERROR" || line == "NO CARRIER" {
			return strings.Join(lines, "\n"), fmt.Errorf("%s: %s", command, line)
		}
	}
}

func (m *Modem) Initialize(ctx context.Context, c config.Modem) error {
	// Force readable result codes, disable hardware auto-answer; the service owns ATA.
	commands := append([]config.Command{{Send: "ATQ0V1", Expect: "OK", TimeoutSeconds: 3}}, c.Init...)
	commands = append(commands, config.Command{Send: "ATE0Q0V1S0=0", Expect: "OK", TimeoutSeconds: 3})
	for _, cmd := range commands {
		deadline, cancel := context.WithTimeout(ctx, time.Duration(cmd.TimeoutSeconds)*time.Second)
		_, err := m.Command(deadline, cmd.Send, cmd.Expect)
		cancel()
		if err != nil {
			return fmt.Errorf("initialize %q: %w", cmd.Send, err)
		}
	}
	return nil
}

func (m *Modem) Answer(ctx context.Context, c config.Modem) (string, error) {
	rings := 0
	for {
		line, _, err := m.line(ctx)
		if err != nil {
			return "", err
		}
		if line != "RING" {
			continue
		}
		rings++
		if rings >= c.AnswerAfterRings {
			break
		}
	}
	if err := m.Write([]byte("ATA\r")); err != nil {
		return "", err
	}
	deadline, cancel := context.WithTimeout(ctx, time.Duration(c.NegotiationSeconds)*time.Second)
	defer cancel()
	for {
		line, delimiter, err := m.line(deadline)
		if err != nil {
			return "", err
		}
		if line == "CONNECT" || strings.HasPrefix(line, "CONNECT ") {
			m.skipLF = delimiter == '\r'
			m.carrier.Store(true)
			return line, nil
		}
		if line == "NO CARRIER" || line == "ERROR" || line == "BUSY" {
			return "", fmt.Errorf("answer failed: %s", line)
		}
	}
}

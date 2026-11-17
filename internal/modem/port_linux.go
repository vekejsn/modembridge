package modem

import (
	"errors"
	"fmt"
	"os"
	"time"

	"go.bug.st/serial"
	"golang.org/x/sys/unix"
	"modembridge/internal/config"
)

// A pollable, nonblocking TTY lets deadlines and Close interrupt both reads and
// CTS-blocked writes. The generic serial library does not expose RTS/CTS setup.
type linuxPort struct {
	file         *os.File
	readTimeout  time.Duration
	writeTimeout time.Duration
}

func openPort(c config.Modem) (Port, error) {
	speeds := map[int]uint32{300: unix.B300, 600: unix.B600, 1200: unix.B1200, 2400: unix.B2400, 4800: unix.B4800,
		9600: unix.B9600, 19200: unix.B19200, 38400: unix.B38400, 57600: unix.B57600, 115200: unix.B115200, 230400: unix.B230400}
	speed, ok := speeds[c.Baud]
	if !ok {
		return nil, fmt.Errorf("unsupported host baud rate %d", c.Baud)
	}
	fd, err := unix.Open(c.Device, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), c.Device)
	failed := true
	defer func() {
		if failed {
			f.Close()
		}
	}()
	if err = unix.IoctlSetInt(fd, unix.TIOCEXCL, 0); err != nil {
		return nil, fmt.Errorf("exclusive modem access: %w", err)
	}
	settings, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	settings.Iflag = 0
	settings.Oflag = 0
	settings.Lflag = 0
	settings.Cflag = unix.CS8 | unix.CREAD | unix.CLOCAL | unix.HUPCL | speed
	if c.HardwareFlowControl {
		settings.Cflag |= unix.CRTSCTS
	}
	settings.Ispeed = speed
	settings.Ospeed = speed
	settings.Cc[unix.VMIN] = 1
	settings.Cc[unix.VTIME] = 0
	if err = unix.IoctlSetTermios(fd, unix.TCSETS, settings); err != nil {
		return nil, err
	}
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCMBIS, unix.TIOCM_RTS); err != nil {
		return nil, fmt.Errorf("assert RTS: %w", err)
	}
	seconds := c.WriteTimeoutSeconds
	if seconds == 0 {
		seconds = 30
	}
	p := &linuxPort{file: f, writeTimeout: time.Duration(seconds) * time.Second}
	// Verify the Go poller supports this device before starting workers.
	if err = f.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return nil, err
	}
	failed = false
	return p, nil
}

func (p *linuxPort) Close() error                         { return p.file.Close() }
func (p *linuxPort) SetReadTimeout(d time.Duration) error { p.readTimeout = d; return nil }
func (p *linuxPort) Read(data []byte) (int, error) {
	if err := p.file.SetReadDeadline(time.Now().Add(p.readTimeout)); err != nil {
		return 0, err
	}
	n, err := p.file.Read(data)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return n, nil
	}
	return n, err
}
func (p *linuxPort) Write(data []byte) (int, error) {
	if err := p.file.SetWriteDeadline(time.Now().Add(p.writeTimeout)); err != nil {
		return 0, err
	}
	return p.file.Write(data)
}
func (p *linuxPort) control(fn func(int) error) error {
	raw, err := p.file.SyscallConn()
	if err != nil {
		return err
	}
	var result error
	err = raw.Control(func(fd uintptr) { result = fn(int(fd)) })
	if err != nil {
		return err
	}
	return result
}
func (p *linuxPort) SetDTR(on bool) error {
	op := uint(unix.TIOCMBIC)
	if on {
		op = unix.TIOCMBIS
	}
	return p.control(func(fd int) error { return unix.IoctlSetPointerInt(fd, op, unix.TIOCM_DTR) })
}
func (p *linuxPort) GetModemStatusBits() (*serial.ModemStatusBits, error) {
	var bits int
	err := p.control(func(fd int) error { var err error; bits, err = unix.IoctlGetInt(fd, unix.TIOCMGET); return err })
	if err != nil {
		return nil, err
	}
	return &serial.ModemStatusBits{CTS: bits&unix.TIOCM_CTS != 0, DSR: bits&unix.TIOCM_DSR != 0, RI: bits&unix.TIOCM_RI != 0, DCD: bits&unix.TIOCM_CAR != 0}, nil
}

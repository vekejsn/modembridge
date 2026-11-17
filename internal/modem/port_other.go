//go:build !linux

package modem

import (
	"errors"
	"go.bug.st/serial"
	"modembridge/internal/config"
)

func openPort(c config.Modem) (Port, error) {
	if c.HardwareFlowControl {
		return nil, errors.New("hardware flow control currently requires Linux")
	}
	return serial.Open(c.Device, &serial.Mode{BaudRate: c.Baud, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})
}

// Package protocol identifies frames without acknowledging or modifying them.
package protocol

import (
	"encoding/binary"
	"errors"
)

var ErrAmbiguous = errors.New("ambiguous protocol")

// detectors is the single registry of supported protocol names and their
// opening-frame detectors. Detectors must not modify the supplied bytes.
var detectors = map[string]func([]byte) bool{
	"millennium": millennium,
	"jatena":     jatena,
}

// Supported reports whether a protocol can be identified.
func Supported(name string) bool {
	_, ok := detectors[name]
	return ok
}

// Detect scans the bounded opening buffer. No match means more input is needed.
// Every byte, including noise before a valid frame, is retained by the caller.
func Detect(data []byte, enabled []string) (string, error) {
	match := ""
	for _, name := range enabled {
		detect, ok := detectors[name]
		if ok && detect(data) {
			if match != "" && match != name {
				return "", ErrAmbiguous
			}
			match = name
		}
	}
	return match, nil
}

func CRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b)
		for range 8 {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xa001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func millennium(data []byte) bool {
	for i := 0; i+6 <= len(data); i++ {
		if data[i] != 2 {
			continue
		}
		// The TTY identification uses a different header from management:
		// STX, length, command, payload, CRC, ETX.
		if data[i+1] == 37 && data[i+2]&0xbf == 0x82 && i+42 <= len(data) {
			f := data[i : i+42]
			if f[3] == 5 && f[9] == 7 && f[17] == 4 && f[22] == 1 && f[24] == 7 &&
				f[41] == 3 && CRC16(f[:39]) == binary.LittleEndian.Uint16(f[39:41]) {
				return true
			}
		}
		if data[i+2] < 5 {
			continue
		}
		n := int(data[i+2]) + 1 // LEN excludes START, includes FLAGS through END.
		if i+n > len(data) {
			continue
		}
		f := data[i : i+n]
		if f[n-1] == 3 && CRC16(f[:n-3]) == binary.LittleEndian.Uint16(f[n-3:n-1]) {
			return true
		}
	}
	return false
}

func jatena(data []byte) bool {
	start := -1
	for i, b := range data {
		if b == 1 {
			start = i
			continue
		}
		if b != 3 || start < 0 {
			continue
		}
		f := data[start : i+1]
		start = -1
		// Original OMC KOM_read echoes 0x07 XID frames before application data.
		// Classify that first exchange so routing does not wait for an ACK that
		// only the backend can supply.
		if len(f) < 6 || (f[1] != 5 && f[1] != 6 && f[1] != 7) {
			continue
		}
		// Match upstream verify_fcc: sum the wire bytes between START and FCC.
		var sum uint16
		for _, v := range f[1 : len(f)-3] {
			sum += uint16(v)
		}
		lo := byte(sum) | 0x80
		hi := byte(sum>>7) | 0x80
		if f[len(f)-3] == lo && f[len(f)-2] == hi {
			return true
		}
	}
	return false
}

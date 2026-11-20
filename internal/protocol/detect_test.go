package protocol

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func bytesFromHex(t *testing.T, value string) []byte {
	t.Helper()
	b, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDetection(t *testing.T) {
	if CRC16([]byte("123456789")) != 0xbb3d {
		t.Fatal("CRC-16/ARC check vector failed")
	}
	// Literal Jatena opening frame: request, sequence 0x70, command 0x20,
	// FCC from sum 0x95. Millennium ACK CRC bytes are populated separately.
	mm := []byte{2, 8, 5}
	crc := CRC16(mm)
	mm = append(mm, byte(crc), byte(crc>>8), 3)
	j := bytesFromHex(t, "01057020958103")
	names := []string{"millennium", "jatena"}
	for _, tc := range []struct {
		name string
		data []byte
	}{{"millennium", mm}, {"jatena", j}} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < len(tc.data); i++ {
				got, err := Detect(tc.data[:i], names)
				if err != nil || got != "" {
					t.Fatalf("premature match at %d: %s %v", i, got, err)
				}
			}
			got, err := Detect(append([]byte{0xff, 0, 0xff}, tc.data...), names)
			if err != nil || got != tc.name {
				t.Fatalf("got %q: %v", got, err)
			}
			bad := append([]byte(nil), tc.data...)
			bad[len(bad)-2] ^= 1
			if got, _ := Detect(bad, names); got != "" {
				t.Fatal("bad checksum accepted")
			}
		})
	}
	if _, err := Detect(append(mm, j...), names); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("expected ambiguity: %v", err)
	}
	if got, _ := Detect(j, []string{"millennium"}); got != "" {
		t.Fatal("disabled profile matched")
	}
}

func TestRegistry(t *testing.T) {
	const name = "test-protocol"
	detectors[name] = func(data []byte) bool { return bytes.Equal(data, []byte("test")) }
	t.Cleanup(func() { delete(detectors, name) })
	if !Supported(name) || Supported("unknown") {
		t.Fatal("registry support lookup failed")
	}
	if got, err := Detect([]byte("test"), []string{name}); got != name || err != nil {
		t.Fatalf("registered detector: got %q, %v", got, err)
	}
	if got, err := Detect([]byte("test"), []string{"unknown"}); got != "" || err != nil {
		t.Fatalf("unknown detector: got %q, %v", got, err)
	}
}

func TestMillenniumPayloadDelimiters(t *testing.T) {
	f := []byte{2, 0, 9, 1, 2, 3, 0x1b}
	crc := CRC16(f)
	f = append(f, byte(crc), byte(crc>>8), 3)
	if got, _ := Detect(f, []string{"millennium"}); got != "millennium" {
		t.Fatal("embedded delimiters broke length-based parsing")
	}
}

func TestMillenniumTTYIdentification(t *testing.T) {
	// Captured TTY identification payload, with its original CRC.
	frame := bytesFromHex(t, "022582051234567890073036434e4230330456312e300101072026111811450621102101230100e57803")
	for i := range frame {
		got, err := Detect(frame[:i], []string{"millennium"})
		if err != nil || got != "" {
			t.Fatalf("premature TTY match at %d: %q %v", i, got, err)
		}
	}
	if got, err := Detect(append([]byte{0xff}, frame...), []string{"millennium"}); got != "millennium" || err != nil {
		t.Fatalf("TTY identification: %q %v", got, err)
	}
	frame[len(frame)-2] ^= 1
	if got, err := Detect(frame, []string{"millennium"}); got != "" || err != nil {
		t.Fatalf("corrupt TTY frame accepted: %q %v", got, err)
	}
}

func TestOriginalJatenaXIDOpening(t *testing.T) {
	// RS232.PRG KOM_read: control 0x07, echoed using the first 21 wire bytes.
	// FCC is literal: sum(0x07 + 16 * 0x20) = 0x0207 => 0x87, 0x84.
	xid := append([]byte{1, 7}, bytes.Repeat([]byte{0x20}, 16)...)
	xid = append(xid, 0x87, 0x84, 3)
	for i := range xid {
		if name, err := Detect(xid[:i], []string{"jatena", "millennium"}); name != "" || err != nil {
			t.Fatalf("premature XID identification at %d: %s %v", i, name, err)
		}
	}
	if name, err := Detect(xid, []string{"jatena", "millennium"}); name != "jatena" || err != nil {
		t.Fatalf("original XID opening not recognized: %s %v", name, err)
	}
}

func FuzzDetect(f *testing.F) {
	f.Add([]byte{1, 5, 0x70, 0x20, 0x95, 0x81, 3})
	f.Add([]byte{2, 0, 5, 0, 0, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65536 {
			t.Skip()
		}
		_, _ = Detect(data, []string{"millennium", "jatena"})
	})
}

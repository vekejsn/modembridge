package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"modembridge/internal/protocol"
)

type Command struct {
	Send           string `json:"send"`
	Expect         string `json:"expect"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type Modem struct {
	Device              string    `json:"device"`
	Baud                int       `json:"baud"`
	HardwareFlowControl bool      `json:"hardware_flow_control"`
	WriteTimeoutSeconds int       `json:"write_timeout_seconds"`
	MonitorCarrier      bool      `json:"monitor_carrier"`
	AnswerAfterRings    int       `json:"answer_after_rings"`
	NegotiationSeconds  int       `json:"negotiation_seconds"`
	Init                []Command `json:"init"`
}

type Route struct {
	Protocol  string `json:"protocol"`
	Mode      string `json:"mode"`
	Address   string `json:"address"`
	Transport string `json:"transport"`
}

type Config struct {
	Modem                  Modem   `json:"modem"`
	Routes                 []Route `json:"routes"`
	IdentificationSeconds  int     `json:"identification_seconds"`
	MaxIdentificationBytes int     `json:"max_identification_bytes"`
	BackendSeconds         int     `json:"backend_seconds"`
	IdleSeconds            int     `json:"idle_seconds"`
	MaxSessionSeconds      int     `json:"max_session_seconds"`
	RecoverySeconds        int     `json:"recovery_seconds"`
}

func Load(path string) (Config, error) {
	c := Config{Modem: Modem{Baud: 19200, HardwareFlowControl: true, WriteTimeoutSeconds: 30, MonitorCarrier: true, AnswerAfterRings: 1, NegotiationSeconds: 45},
		IdentificationSeconds: 10, MaxIdentificationBytes: 65536, BackendSeconds: 5,
		IdleSeconds: 120, MaxSessionSeconds: 1800, RecoverySeconds: 2}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, errors.New("expected one JSON configuration object")
	}
	return c, c.Validate()
}

func (c *Config) Validate() error {
	if c.Modem.WriteTimeoutSeconds == 0 {
		c.Modem.WriteTimeoutSeconds = 30
	}
	if c.Modem.WriteTimeoutSeconds < 1 || c.Modem.WriteTimeoutSeconds > 86400 {
		return errors.New("write_timeout_seconds must be between 1 and 86400")
	}
	if c.Modem.Device == "" || c.Modem.Baud <= 0 {
		return errors.New("modem.device and positive modem.baud are required")
	}
	if c.Modem.AnswerAfterRings < 1 {
		return errors.New("answer_after_rings must be positive")
	}
	for name, value := range map[string]int{"negotiation_seconds": c.Modem.NegotiationSeconds, "identification_seconds": c.IdentificationSeconds,
		"backend_seconds": c.BackendSeconds, "idle_seconds": c.IdleSeconds, "max_session_seconds": c.MaxSessionSeconds, "recovery_seconds": c.RecoverySeconds} {
		if value < 1 || value > 86400 {
			return fmt.Errorf("%s must be between 1 and 86400", name)
		}
	}
	if c.MaxIdentificationBytes < 6 || c.MaxIdentificationBytes > 1<<20 {
		return errors.New("max_identification_bytes must be between 6 and 1048576")
	}
	for i := range c.Modem.Init {
		cmd := &c.Modem.Init[i]
		if cmd.Send == "" || strings.ContainsAny(cmd.Send, "\r\n") {
			return fmt.Errorf("invalid init command %d", i)
		}
		if cmd.Expect == "" {
			cmd.Expect = "OK"
		}
		if cmd.TimeoutSeconds == 0 {
			cmd.TimeoutSeconds = 3
		}
		if cmd.TimeoutSeconds < 1 || cmd.TimeoutSeconds > 60 {
			return fmt.Errorf("invalid command timeout %d", i)
		}
	}
	if len(c.Routes) == 0 {
		return errors.New("at least one route is required")
	}
	seen := map[string]bool{}
	for i := range c.Routes {
		r := &c.Routes[i]
		if !protocol.Supported(r.Protocol) {
			return fmt.Errorf("unsupported protocol %q", r.Protocol)
		}
		if seen[r.Protocol] {
			return fmt.Errorf("duplicate route for %s", r.Protocol)
		}
		seen[r.Protocol] = true
		if r.Mode == "" {
			r.Mode = "connect"
		}
		if r.Mode != "connect" && r.Mode != "listen" {
			return fmt.Errorf("invalid route mode %q", r.Mode)
		}
		if r.Transport == "" {
			r.Transport = "raw"
		}
		if r.Transport != "raw" && r.Transport != "enriched" {
			return fmt.Errorf("invalid transport %q", r.Transport)
		}
		host, port, err := net.SplitHostPort(r.Address)
		portNumber, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || (r.Mode == "connect" && host == "") {
			return fmt.Errorf("invalid backend address %q", r.Address)
		}
	}
	return nil
}

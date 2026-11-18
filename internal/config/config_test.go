package config

import (
	"strings"
	"testing"
)

func TestProtocolValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr bool
	}{
		{"millennium", false},
		{"jatena", false},
		{"unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{
				Modem:                 Modem{Device: "test", Baud: 19200, AnswerAfterRings: 1, NegotiationSeconds: 1},
				Routes:                []Route{{Protocol: tc.name, Address: "127.0.0.1:1234"}},
				IdentificationSeconds: 1, MaxIdentificationBytes: 6, BackendSeconds: 1,
				IdleSeconds: 1, MaxSessionSeconds: 1, RecoverySeconds: 1,
			}
			err := c.Validate()
			if tc.wantErr && (err == nil || !strings.Contains(err.Error(), "unsupported protocol")) {
				t.Fatalf("expected unsupported protocol error, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

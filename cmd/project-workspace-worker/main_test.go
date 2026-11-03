package main

import (
	"strings"
	"testing"
)

func TestPairingCode(t *testing.T) {
	tests := []struct {
		name     string
		flag     string
		fromIn   bool
		input    string
		want     string
		wantFail bool
	}{
		{name: "explicit flag", flag: "example", want: "example"},
		{name: "standard input", fromIn: true, input: "example\n", want: "example"},
		{name: "spaces and line endings", fromIn: true, input: "  example\r\n", want: "example"},
		{name: "missing", wantFail: true},
		{name: "empty input", fromIn: true, wantFail: true},
		{name: "conflicting sources", flag: "example", fromIn: true, input: "other", wantFail: true},
		{name: "oversized input", fromIn: true, input: strings.Repeat("x", 257), wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pairingCode(tt.flag, tt.fromIn, strings.NewReader(tt.input))
			if (err != nil) != tt.wantFail {
				t.Fatalf("pairingCode() error = %v; want failure %t", err, tt.wantFail)
			}
			if !tt.wantFail && got != tt.want {
				t.Fatalf("pairingCode() = %q; want %q", got, tt.want)
			}
		})
	}
}

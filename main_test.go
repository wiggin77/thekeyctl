package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// captureStdout redirects the program's stdout for the duration of the test.
func captureStdout(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer

	previous := stdout
	stdout = &buf

	t.Cleanup(func() { stdout = previous })

	return &buf
}

// captureOutput redirects both streams for the duration of the test.
func captureOutput(t *testing.T) (out, errOut *bytes.Buffer) {
	t.Helper()

	var outBuf, errBuf bytes.Buffer

	previousOut, previousErr := stdout, stderr
	stdout, stderr = &outBuf, &errBuf

	t.Cleanup(func() { stdout, stderr = previousOut, previousErr })

	return &outBuf, &errBuf
}

// TestParseCommandHelpUsesStdout covers the whole point of parsing before the
// device is opened: help is answered with no hardware and goes to stdout so it
// can be piped.
func TestParseCommandHelpUsesStdout(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			out, errOut := captureOutput(t)

			h, err := parseCommand([]string{arg})
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}

			if h != nil {
				t.Error("handler is not nil, want nil so the device is never opened")
			}

			if !strings.Contains(out.String(), "Usage:") {
				t.Errorf("stdout = %q, want the usage text", out.String())
			}

			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut.String())
			}
		})
	}
}

func TestParseCommandErrorsUseStderr(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no arguments", nil, "command required"},
		{"unknown command", []string{"bogus"}, `unknown command "bogus"`},
		{"device without subcommand", []string{"device"}, "device command required"},
		{"unknown device command", []string{"device", "bogus"}, `unknown device command "bogus"`},
		{"device info with arguments", []string{"device", "info", "extra"}, `unexpected argument "extra"`},
		{"unknown led command", []string{"led", "bogus"}, `unknown LED command "bogus"`},
		{"missing led command", []string{"led"}, "LED command required"},
		{"status with arguments", []string{"status", "extra"}, `unexpected argument "extra"`},
		{"led off with arguments", []string{"led", "off", "extra"}, "led off takes no arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut := captureOutput(t)

			h, err := parseCommand(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}

			if h != nil {
				t.Error("handler is not nil, want nil")
			}

			if !strings.Contains(errOut.String(), "Usage") {
				t.Errorf("stderr = %q, want the usage text", errOut.String())
			}

			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", out.String())
			}
		})
	}
}

func TestParseCommandRejectsBadFlagsWithoutDevice(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown color", []string{"led", "solid", "--color", "chartreuse"}, `unknown color "chartreuse"`},
		{"brightness too high", []string{"led", "solid", "--brightness", "300"}, "brightness must be between 0 and 255"},
		{"negative brightness", []string{"led", "alert", "--brightness", "-1"}, "brightness must be between 0 and 255"},
		{"rate too low", []string{"led", "alert", "--rate", "0"}, "rate must be between 1 and 4"},
		{"rate too high", []string{"led", "alert", "--rate", "5"}, "rate must be between 1 and 4"},
		{"unexpected argument", []string{"led", "solid", "extra"}, `unexpected argument "extra"`},
		{"undefined flag", []string{"led", "alert", "-bogus"}, "not defined"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := captureOutput(t)

			h, err := parseCommand(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}

			if h != nil {
				t.Error("handler is not nil, want nil")
			}

			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", out.String())
			}
		})
	}
}

func TestParseCommandSubcommandHelpUsesStdout(t *testing.T) {
	tests := []struct {
		args    []string
		wantStr string
	}{
		{[]string{"led", "solid", "-h"}, "-brightness"},
		{[]string{"led", "alert", "--help"}, "-brightness"},
		{[]string{"status", "-h"}, "-json"},
		{[]string{"device", "info", "--help"}, "-json"},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			out, errOut := captureOutput(t)

			h, err := parseCommand(tt.args)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}

			if h != nil {
				t.Error("handler is not nil, want nil")
			}

			if !strings.Contains(out.String(), tt.wantStr) {
				t.Errorf("stdout = %q, want %q in output", out.String(), tt.wantStr)
			}

			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut.String())
			}
		})
	}
}

// TestParseCommandBuildsHandlers runs each parsed handler against a fake
// device, so the flags are checked all the way through to the bytes on the
// wire.
func TestParseCommandBuildsHandlers(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want [][]byte
	}{
		{
			name: "led off",
			args: []string{"led", "off"},
			want: [][]byte{{cmdLightingSetValue, rgbEffect, effectOff}},
		},
		{
			name: "solid defaults to white at 64",
			args: []string{"led", "solid"},
			want: [][]byte{
				{cmdLightingSetValue, rgbEffect, effectStatic},
				{cmdLightingSetValue, rgbBrightness, 0},
				{cmdLightingSetValue, rgbColor, 0, 0},
				{cmdLightingSetValue, rgbBrightness, 64},
			},
		},
		{
			name: "solid with flags",
			args: []string{"led", "solid", "--color", "blue", "--brightness", "30"},
			want: [][]byte{
				{cmdLightingSetValue, rgbEffect, effectStatic},
				{cmdLightingSetValue, rgbBrightness, 0},
				{cmdLightingSetValue, rgbColor, 170, 255},
				{cmdLightingSetValue, rgbBrightness, 30},
			},
		},
		{
			name: "alert defaults to amber breathing at 80",
			args: []string{"led", "alert"},
			want: [][]byte{
				{cmdLightingSetValue, rgbEffect, effectStatic},
				{cmdLightingSetValue, rgbBrightness, 0},
				{cmdLightingSetValue, rgbColor, 21, 255},
				{cmdLightingSetValue, rgbBrightness, 80},
				{cmdLightingSetValue, rgbEffect, effectBreathing},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := parseCommand(tt.args)
			if err != nil {
				t.Fatalf("parseCommand: %v", err)
			}

			if h == nil {
				t.Fatal("handler is nil, want one")
			}

			d, f := echoDevice()

			if err := h(d); err != nil {
				t.Fatalf("handler: %v", err)
			}

			wantWrites(t, f, tt.want)
		})
	}
}

// TestParseLEDAlertRates covers the rate-to-mode mapping, which depends on QMK
// assigning the breathing effect four consecutive modes.
func TestParseLEDAlertRates(t *testing.T) {
	for rate := 1; rate <= 4; rate++ {
		h, err := parseLEDAlert([]string{"--rate", strconv.Itoa(rate)})
		if err != nil {
			t.Fatalf("rate %d: %v", rate, err)
		}

		d, f := echoDevice()

		if err := h(d); err != nil {
			t.Fatalf("rate %d: %v", rate, err)
		}

		writes := f.payloads(4)
		got := writes[len(writes)-1][2]

		if want := effectBreathing + byte(rate-1); got != want {
			t.Errorf("rate %d set effect %d, want %d", rate, got, want)
		}
	}
}

func TestParseStatusBuildsHandlers(t *testing.T) {
	for _, args := range [][]string{
		{"status"},
		{"status", "--json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h, err := parseCommand(args)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}

			if h == nil {
				t.Fatal("handler is nil, want one")
			}
		})
	}
}

func TestParseDeviceInfoBuildsHandlers(t *testing.T) {
	for _, args := range [][]string{
		{"device", "info"},
		{"device", "info", "--json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h, err := parseCommand(args)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}

			if h == nil {
				t.Fatal("handler is nil, want one")
			}
		})
	}
}

func TestUint8Arg(t *testing.T) {
	tests := []struct {
		value   int
		wantErr bool
	}{
		{0, false},
		{255, false},
		{-1, true},
		{256, true},
	}

	for _, tt := range tests {
		got, err := uint8Arg("brightness", tt.value)

		if tt.wantErr {
			if err == nil {
				t.Errorf("uint8Arg(%d) succeeded, want an error", tt.value)
			}

			continue
		}

		if err != nil {
			t.Errorf("uint8Arg(%d): %v", tt.value, err)
			continue
		}

		if int(got) != tt.value {
			t.Errorf("uint8Arg(%d) = %d", tt.value, got)
		}
	}
}

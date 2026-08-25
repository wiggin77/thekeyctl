package main

import (
	"bytes"
	"strings"
	"testing"
)

// echoDevice returns a device whose transport answers every command with a
// well-formed response, so tests can assert on what was written.
func echoDevice() (*device, *fakeHID) {
	f := &fakeHID{autoEcho: true}

	return &device{hid: f}, f
}

// wantWrites compares the recorded lighting payloads, which are all 4 bytes or
// shorter, against the expected sequence.
func wantWrites(t *testing.T, f *fakeHID, want [][]byte) {
	t.Helper()

	got := f.payloads(4)

	if len(got) != len(want) {
		t.Fatalf("got %d writes, want %d:\ngot  % x\nwant % x", len(got), len(want), got, want)
	}

	for i := range want {
		// The recorded payload is padded out to 4 bytes.
		padded := make([]byte, 4)
		copy(padded, want[i])

		if !bytes.Equal(got[i], padded) {
			t.Errorf("write %d = % x, want % x", i, got[i], padded)
		}
	}
}

// TestApplyLightingWriteOrder locks down the order the firmware requires.
// RGBLIGHT ignores writes it cannot apply to its current state, so lighting
// has to be enabled and configured in static mode before the effect is set.
func TestApplyLightingWriteOrder(t *testing.T) {
	t.Run("breathing", func(t *testing.T) {
		d, f := echoDevice()

		if err := applyLighting(d, hsv{h: 85, s: 255}, 100, effectBreathing+1); err != nil {
			t.Fatalf("applyLighting: %v", err)
		}

		wantWrites(t, f, [][]byte{
			{cmdLightingSetValue, rgbEffect, effectStatic},
			{cmdLightingSetValue, rgbBrightness, 0},
			{cmdLightingSetValue, rgbColor, 85, 255},
			{cmdLightingSetValue, rgbBrightness, 100},
			{cmdLightingSetValue, rgbEffect, effectBreathing + 1},
		})
	})

	t.Run("static skips the redundant effect write", func(t *testing.T) {
		d, f := echoDevice()

		if err := applyLighting(d, hsv{h: 170, s: 255}, 30, effectStatic); err != nil {
			t.Fatalf("applyLighting: %v", err)
		}

		wantWrites(t, f, [][]byte{
			{cmdLightingSetValue, rgbEffect, effectStatic},
			{cmdLightingSetValue, rgbBrightness, 0},
			{cmdLightingSetValue, rgbColor, 170, 255},
			{cmdLightingSetValue, rgbBrightness, 30},
		})
	})
}

func TestApplyLightingStopsOnError(t *testing.T) {
	d, f := newFakeDevice(
		fakeRead{data: packet(cmdLightingSetValue, rgbEffect)},
		fakeRead{data: packet(cmdLightingSetValue, rgbBrightness)},
		fakeRead{data: packet(0xff)},
	)

	err := applyLighting(d, hsv{h: 85, s: 255}, 100, effectBreathing)
	if err == nil || !strings.Contains(err.Error(), "setting color") {
		t.Fatalf("err = %v, want a color-write error", err)
	}

	if len(f.writes) != 3 {
		t.Errorf("got %d writes, want 3; the sequence must stop at the failure", len(f.writes))
	}
}

func TestLedOff(t *testing.T) {
	d, f := echoDevice()

	if err := ledOff(d); err != nil {
		t.Fatalf("ledOff: %v", err)
	}

	wantWrites(t, f, [][]byte{{cmdLightingSetValue, rgbEffect, effectOff}})
}

func TestParseColor(t *testing.T) {
	got, err := parseColor("blue")
	if err != nil {
		t.Fatalf("parseColor: %v", err)
	}

	if got != (hsv{h: 170, s: 255}) {
		t.Errorf("blue = %+v, want {h:170 s:255}", got)
	}

	if _, err := parseColor("chartreuse"); err == nil {
		t.Error("err = nil, want an unknown-color error")
	}
}

// TestParseColorNamesMatchUsage guards against adding a color to one list and
// forgetting the other.
func TestParseColorNamesMatchUsage(t *testing.T) {
	var buf bytes.Buffer
	fprintUsage(&buf)
	text := buf.String()

	for name := range colors {
		if !strings.Contains(text, "\n  "+name+"\n") {
			t.Errorf("color %q is missing from the usage text", name)
		}
	}
}

func TestStatusOutput(t *testing.T) {
	d, _ := newFakeDevice(
		fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)},
		fakeRead{data: packet(cmdLightingGetValue, rgbEffect, 3)},
		fakeRead{data: packet(cmdLightingGetValue, rgbBrightness, 77)},
		fakeRead{data: packet(cmdLightingGetValue, rgbColor, 191, 255)},
		fakeRead{data: packet(cmdLightingGetValue, rgbSpeed, 0)},
	)

	out := captureStdout(t)

	if err := status(d); err != nil {
		t.Fatalf("status: %v", err)
	}

	want := "protocol:   9\neffect:     3\nbrightness: 77\nhue:        191\nsaturation: 255\nspeed:      0\n"
	if got := out.String(); got != want {
		t.Errorf("status output =\n%q\nwant\n%q", got, want)
	}
}

func TestStatusReportsWhichReadFailed(t *testing.T) {
	d, _ := newFakeDevice(
		fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)},
		fakeRead{data: packet(cmdLightingGetValue, rgbEffect, 3)},
		fakeRead{data: packet(0xff)},
	)

	captureStdout(t)

	err := status(d)
	if err == nil || !strings.Contains(err.Error(), "getting brightness") {
		t.Fatalf("err = %v, want a brightness-read error", err)
	}
}

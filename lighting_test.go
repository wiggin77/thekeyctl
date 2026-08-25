package main

import (
	"bytes"
	"encoding/json"
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
func TestDeviceInfo(t *testing.T) {
	d, _ := newFakeDevice(
		fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)},
		fakeRead{data: packet(cmdLightingGetValue, rgbBrightness, 40)},
	)
	d.path = "/dev/hidraw0"
	d.product = "Drop The Key V2"

	out := captureStdout(t)

	if err := deviceInfo(d); err != nil {
		t.Fatalf("deviceInfo: %v", err)
	}

	want := "Device:       Drop The Key V2\n" +
		"VID:PID:      359b:000e\n" +
		"HID path:     /dev/hidraw0\n" +
		"VIA protocol: 9\n" +
		"RGBLIGHT:     available\n"
	if got := out.String(); got != want {
		t.Errorf("deviceInfo output =\n%q\nwant\n%q", got, want)
	}
}

func TestDeviceInfoFallsBackWhenProductStringEmpty(t *testing.T) {
	d, _ := newFakeDevice(
		fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)},
		fakeRead{data: packet(cmdLightingGetValue, rgbBrightness, 0)},
	)
	d.path = "/dev/hidraw0"

	out := captureStdout(t)

	if err := deviceInfo(d); err != nil {
		t.Fatalf("deviceInfo: %v", err)
	}

	if !strings.Contains(out.String(), "Device:       unknown\n") {
		t.Errorf("output = %q, want fallback device name \"unknown\"", out.String())
	}
}

func TestDeviceInfoRGBLIGHTUnavailable(t *testing.T) {
	d, _ := newFakeDevice(
		fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)},
		fakeRead{data: packet(0xff)},
	)
	d.path = "/dev/hidraw0"
	d.product = "Drop The Key V2"

	out := captureStdout(t)

	if err := deviceInfo(d); err != nil {
		t.Fatalf("deviceInfo: %v", err)
	}

	if !strings.Contains(out.String(), "RGBLIGHT:     not available\n") {
		t.Errorf("output = %q, want \"not available\"", out.String())
	}
}

func TestStatusJSON(t *testing.T) {
	d, _ := newFakeDevice(
		fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)},
		fakeRead{data: packet(cmdLightingGetValue, rgbEffect, 3)},
		fakeRead{data: packet(cmdLightingGetValue, rgbBrightness, 77)},
		fakeRead{data: packet(cmdLightingGetValue, rgbColor, 191, 255)},
		fakeRead{data: packet(cmdLightingGetValue, rgbSpeed, 0)},
	)

	out := captureStdout(t)

	if err := statusJSON(d); err != nil {
		t.Fatalf("statusJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %q", err, out.String())
	}

	checks := map[string]float64{
		"protocol_version": 9,
		"effect":           3,
		"brightness":       77,
		"hue":              191,
		"saturation":       255,
		"speed":            0,
	}
	for key, want := range checks {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
}

func TestDeviceInfoJSON(t *testing.T) {
	d, _ := newFakeDevice(
		fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)},
		fakeRead{data: packet(cmdLightingGetValue, rgbBrightness, 40)},
	)
	d.path = "/dev/hidraw0"
	d.product = "Drop The Key V2"

	out := captureStdout(t)

	if err := deviceInfoJSON(d); err != nil {
		t.Fatalf("deviceInfoJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %q", err, out.String())
	}

	if got["product"] != "Drop The Key V2" {
		t.Errorf("product = %v, want \"Drop The Key V2\"", got["product"])
	}
	if got["vid"] != "359b" {
		t.Errorf("vid = %v, want \"359b\"", got["vid"])
	}
	if got["pid"] != "000e" {
		t.Errorf("pid = %v, want \"000e\"", got["pid"])
	}
	if got["hid_path"] != "/dev/hidraw0" {
		t.Errorf("hid_path = %v, want \"/dev/hidraw0\"", got["hid_path"])
	}
	if got["via_protocol"] != float64(9) {
		t.Errorf("via_protocol = %v, want 9", got["via_protocol"])
	}
	if got["rgblight_available"] != true {
		t.Errorf("rgblight_available = %v, want true", got["rgblight_available"])
	}
}

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
		if !strings.Contains(text, name) {
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

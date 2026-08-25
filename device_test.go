package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	hid "github.com/sstallion/go-hid"
)

// errEINTR is what go-hid hands back when a signal interrupts a transfer: a
// plain error carrying strerror(EINTR), with no errno attached.
var errEINTR = errors.New("Interrupted system call")

// fakeRead is one scripted response to a ReadWithTimeout call.
type fakeRead struct {
	data []byte
	err  error
}

// fakeHID is a scripted transport. Reads and write errors are consumed from
// queues in order, so a test can describe a whole exchange, including retries.
type fakeHID struct {
	writes   [][]byte
	writeQ   []error
	readQ    []fakeRead
	timeouts []time.Duration
	closed   bool

	// autoEcho answers every write with a well-formed response echoing the
	// command byte and, for lighting commands, the value ID.
	autoEcho bool

	// onWrite overrides the byte count reported for a write.
	onWrite func(p []byte) (int, error)
}

func (f *fakeHID) Write(p []byte) (int, error) {
	f.writes = append(f.writes, append([]byte(nil), p...))

	if f.autoEcho {
		// p[0] is the report ID, so the payload starts at p[1].
		f.readQ = append(f.readQ, fakeRead{data: packet(p[1], p[2])})
	}

	if f.onWrite != nil {
		return f.onWrite(p)
	}

	if len(f.writeQ) > 0 {
		err := f.writeQ[0]
		f.writeQ = f.writeQ[1:]

		if err != nil {
			return -1, err
		}
	}

	return len(p), nil
}

func (f *fakeHID) ReadWithTimeout(p []byte, timeout time.Duration) (int, error) {
	f.timeouts = append(f.timeouts, timeout)

	if len(f.readQ) == 0 {
		return 0, hid.ErrTimeout
	}

	r := f.readQ[0]
	f.readQ = f.readQ[1:]

	if r.err != nil {
		return -1, r.err
	}

	return copy(p, r.data), nil
}

func (f *fakeHID) Close() error {
	f.closed = true
	return nil
}

// payloads returns the QMK payloads of the recorded writes, with the leading
// report-ID byte and the trailing padding removed.
func (f *fakeHID) payloads(length int) [][]byte {
	out := make([][]byte, 0, len(f.writes))

	for _, w := range f.writes {
		out = append(out, w[1:1+length])
	}

	return out
}

// packet builds a 32-byte response starting with the given bytes.
func packet(b ...byte) []byte {
	p := make([]byte, packetSize)
	copy(p, b)

	return p
}

func newFakeDevice(reads ...fakeRead) (*device, *fakeHID) {
	f := &fakeHID{readQ: reads}

	return &device{hid: f}, f
}

func TestCommandFramesReport(t *testing.T) {
	d, f := newFakeDevice(fakeRead{data: packet(cmdLightingSetValue, rgbBrightness)})

	if _, err := d.command(cmdLightingSetValue, rgbBrightness, 40); err != nil {
		t.Fatalf("command: %v", err)
	}

	if len(f.writes) != 1 {
		t.Fatalf("got %d writes, want 1", len(f.writes))
	}

	out := f.writes[0]

	if len(out) != packetSize+1 {
		t.Errorf("wrote %d bytes, want %d", len(out), packetSize+1)
	}

	if out[0] != 0 {
		t.Errorf("report ID = 0x%02x, want 0x00", out[0])
	}

	want := []byte{cmdLightingSetValue, rgbBrightness, 40}
	if got := out[1 : 1+len(want)]; !bytes.Equal(got, want) {
		t.Errorf("payload = % x, want % x", got, want)
	}

	for i, b := range out[1+len(want):] {
		if b != 0 {
			t.Errorf("padding byte %d = 0x%02x, want 0x00", i, b)
			break
		}
	}
}

func TestCommandRejectsBadPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{"empty", nil, "empty HID command"},
		{"too large", make([]byte, packetSize+1), "HID command too large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, f := newFakeDevice()

			_, err := d.command(tt.payload...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}

			if len(f.writes) != 0 {
				t.Errorf("got %d writes, want none", len(f.writes))
			}
		})
	}
}

func TestCommandShortWrite(t *testing.T) {
	d, f := newFakeDevice()
	f.onWrite = func(p []byte) (int, error) { return len(p) - 1, nil }

	_, err := d.command(cmdGetProtocolVersion)
	if err == nil || !strings.Contains(err.Error(), "short HID write") {
		t.Fatalf("err = %v, want a short-write error", err)
	}
}

func TestCommandUnhandledByDevice(t *testing.T) {
	d, _ := newFakeDevice(fakeRead{data: packet(0xff)})

	_, err := d.command(cmdLightingSetValue, rgbEffect, 1)
	if err == nil || !strings.Contains(err.Error(), "unhandled") {
		t.Fatalf("err = %v, want an unhandled-command error", err)
	}
}

func TestCommandRejectsMismatchedEcho(t *testing.T) {
	d, _ := newFakeDevice(fakeRead{data: packet(cmdLightingGetValue)})

	_, err := d.command(cmdGetProtocolVersion)
	if err == nil || !strings.Contains(err.Error(), "unexpected VIA response command") {
		t.Fatalf("err = %v, want a response-mismatch error", err)
	}
}

func TestCommandTimeout(t *testing.T) {
	d, f := newFakeDevice()

	_, err := d.command(cmdGetProtocolVersion)
	if err == nil || err.Error() != "timeout waiting for HID response" {
		t.Fatalf("err = %v, want a timeout error", err)
	}

	if len(f.timeouts) != 1 || f.timeouts[0] != responseTimeout {
		t.Errorf("read timeouts = %v, want one of %v", f.timeouts, responseTimeout)
	}
}

func TestCommandRetriesInterruptedRead(t *testing.T) {
	d, f := newFakeDevice(
		fakeRead{err: errEINTR},
		fakeRead{err: errEINTR},
		fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)},
	)

	resp, err := d.command(cmdGetProtocolVersion)
	if err != nil {
		t.Fatalf("command: %v", err)
	}

	if resp[2] != 0x09 {
		t.Errorf("resp[2] = 0x%02x, want 0x09", resp[2])
	}

	if len(f.timeouts) != 3 {
		t.Errorf("got %d read attempts, want 3", len(f.timeouts))
	}

	if len(f.writes) != 1 {
		t.Errorf("got %d writes, want 1; the report must not be re-sent", len(f.writes))
	}
}

func TestCommandRetriesInterruptedWrite(t *testing.T) {
	d, f := newFakeDevice(fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)})
	f.writeQ = []error{errEINTR, errEINTR, nil}

	if _, err := d.command(cmdGetProtocolVersion); err != nil {
		t.Fatalf("command: %v", err)
	}

	if len(f.writes) != 3 {
		t.Errorf("got %d write attempts, want 3", len(f.writes))
	}
}

func TestCommandGivesUpAfterMaxInterruptRetries(t *testing.T) {
	reads := make([]fakeRead, maxInterruptRetries+1)
	for i := range reads {
		reads[i] = fakeRead{err: errEINTR}
	}

	d, f := newFakeDevice(reads...)

	_, err := d.command(cmdGetProtocolVersion)
	if err == nil || !strings.Contains(err.Error(), "Interrupted system call") {
		t.Fatalf("err = %v, want the interrupted-read error to surface", err)
	}

	if want := maxInterruptRetries + 1; len(f.timeouts) != want {
		t.Errorf("got %d read attempts, want %d", len(f.timeouts), want)
	}
}

func TestCommandDoesNotRetryOtherErrors(t *testing.T) {
	d, f := newFakeDevice(
		fakeRead{err: errors.New("No such device")},
		fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)},
	)

	if _, err := d.command(cmdGetProtocolVersion); err == nil {
		t.Fatal("err = nil, want the device error to surface")
	}

	if len(f.timeouts) != 1 {
		t.Errorf("got %d read attempts, want 1", len(f.timeouts))
	}
}

func TestIsEINTR(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"hidapi strerror text", errEINTR, true},
		{"lowercase", errors.New("interrupted system call"), true},
		{"wrapped errno", fmt.Errorf("reading: %w", syscall.EINTR), true},
		{"other hidapi error", errors.New("No such device"), false},
		{"timeout", hid.ErrTimeout, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEINTR(tt.err); got != tt.want {
				t.Errorf("isEINTR(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestProtocolVersion(t *testing.T) {
	d, _ := newFakeDevice(fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x09)})

	version, err := d.protocolVersion()
	if err != nil {
		t.Fatalf("protocolVersion: %v", err)
	}

	if version != expectedVIAProtocol {
		t.Errorf("version = %d, want %d", version, expectedVIAProtocol)
	}
}

func TestCheckProtocolRejectsOtherVersions(t *testing.T) {
	d, _ := newFakeDevice(fakeRead{data: packet(cmdGetProtocolVersion, 0x00, 0x0c)})

	err := d.checkProtocol()
	if err == nil || !strings.Contains(err.Error(), "unsupported VIA protocol version 12") {
		t.Fatalf("err = %v, want an unsupported-version error", err)
	}
}

func TestSetRGBRejectsMismatchedValueID(t *testing.T) {
	d, _ := newFakeDevice(fakeRead{data: packet(cmdLightingSetValue, rgbEffect)})

	err := d.setBrightness(40)
	if err == nil || !strings.Contains(err.Error(), "unexpected RGBLIGHT value ID") {
		t.Fatalf("err = %v, want a value-ID mismatch error", err)
	}
}

func TestGetRGBAccessors(t *testing.T) {
	t.Run("brightness", func(t *testing.T) {
		d, f := newFakeDevice(fakeRead{data: packet(cmdLightingGetValue, rgbBrightness, 40)})

		got, err := d.getBrightness()
		if err != nil {
			t.Fatalf("getBrightness: %v", err)
		}

		if got != 40 {
			t.Errorf("brightness = %d, want 40", got)
		}

		want := []byte{cmdLightingGetValue, rgbBrightness}
		if got := f.payloads(2)[0]; !bytes.Equal(got, want) {
			t.Errorf("payload = % x, want % x", got, want)
		}
	})

	t.Run("color", func(t *testing.T) {
		d, _ := newFakeDevice(fakeRead{data: packet(cmdLightingGetValue, rgbColor, 170, 255)})

		got, err := d.getColor()
		if err != nil {
			t.Fatalf("getColor: %v", err)
		}

		if got != (hsv{h: 170, s: 255}) {
			t.Errorf("color = %+v, want {h:170 s:255}", got)
		}
	})
}

func TestGetRGBRejectsShortResponse(t *testing.T) {
	d, _ := newFakeDevice(fakeRead{data: []byte{cmdLightingGetValue, rgbColor, 170}})

	_, err := d.getColor()
	if err == nil || !strings.Contains(err.Error(), "short RGBLIGHT get response") {
		t.Fatalf("err = %v, want a short-response error", err)
	}
}

func TestCloseClosesTransport(t *testing.T) {
	d, f := newFakeDevice()

	if err := d.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if !f.closed {
		t.Error("transport was not closed")
	}
}

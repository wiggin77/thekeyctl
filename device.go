package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	hid "github.com/sstallion/go-hid"
)

const (
	vendorID  uint16 = 0x359b
	productID uint16 = 0x000e

	// QMK Raw HID interface.
	rawUsagePage uint16 = 0xff60
	rawUsage     uint16 = 0x0061

	// QMK Raw HID packets are always 32 bytes.
	packetSize = 32

	// How long to wait for the device to answer a single command.
	responseTimeout = time.Second

	// How many times a HID transfer is retried after a signal interrupts it.
	maxInterruptRetries = 5

	// VIA protocol commands used by the Vial-QMK firmware.
	cmdGetProtocolVersion byte = 0x01
	cmdLightingSetValue   byte = 0x07
	cmdLightingGetValue   byte = 0x08

	// Legacy VIA protocol RGBLIGHT value IDs used by Vial-QMK
	// VIA protocol version 9.
	rgbBrightness byte = 0x80
	rgbEffect     byte = 0x81
	rgbSpeed      byte = 0x82
	rgbColor      byte = 0x83

	expectedVIAProtocol uint16 = 0x0009
)

// transport is the part of *hid.Device this program uses. Depending on the
// interface rather than the concrete type lets the packet framing, the
// response checks and the retry logic below be tested without a macropad.
type transport interface {
	Write(p []byte) (int, error)
	ReadWithTimeout(p []byte, timeout time.Duration) (int, error)
	Close() error
}

type device struct {
	hid     transport
	path    string
	product string
}

func openDevice() (*device, error) {
	var path, product string

	err := hid.Enumerate(vendorID, productID, func(info *hid.DeviceInfo) error {
		if path != "" {
			return nil
		}

		if info.UsagePage == rawUsagePage && info.Usage == rawUsage {
			path = info.Path
			product = info.ProductStr
		}

		return nil
	})
	if err != nil {
		return nil, &codeError{
			code: exitHIDFailure,
			err:  fmt.Errorf("enumerating HID devices: %w", err),
		}
	}

	if path == "" {
		return nil, &codeError{
			code: exitDeviceNotFound,
			err: fmt.Errorf(
				"The Key V2 Vial HID interface not found (VID=%04x PID=%04x usage=%04x:%04x)",
				vendorID,
				productID,
				rawUsagePage,
				rawUsage,
			),
		}
	}

	// Probe r/w access before handing off to HIDAPI. os.OpenFile returns a
	// typed *os.PathError wrapping os.ErrPermission on EACCES, so we get a
	// structured error rather than having to match HIDAPI's opaque strings.
	if f, err := os.OpenFile(path, os.O_RDWR, 0); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, &codeError{
				code: exitAccessDenied,
				err:  fmt.Errorf("opening HID device %q: permission denied (check udev rules)", path),
			}
		}
	} else {
		f.Close()
	}

	d, err := hid.OpenPath(path)
	if err != nil {
		return nil, &codeError{
			code: exitHIDFailure,
			err:  fmt.Errorf("opening HID device %q: %w", path, err),
		}
	}

	return &device{hid: d, path: path, product: product}, nil
}

func (d *device) close() error {
	return d.hid.Close()
}

// isEINTR reports whether err is an "interrupted system call" failure.
//
// HIDAPI reports a failed read(2), write(2) or poll(2) by formatting errno
// with strerror(3), and go-hid turns that into a plain error carrying only the
// message, so there is no errno left to compare against. The message is
// matched instead, against the same text the syscall package uses. errors.Is
// is still tried first, in case a future go-hid wraps a real syscall.Errno.
func isEINTR(err error) bool {
	if errors.Is(err, syscall.EINTR) {
		return true
	}

	return strings.EqualFold(err.Error(), syscall.EINTR.Error())
}

// writeReport writes one HID report, retrying when a signal interrupts the
// write. An interrupted write(2) transferred nothing, so the retry cannot
// duplicate a command.
func (d *device) writeReport(out []byte) (int, error) {
	for attempt := 0; ; attempt++ {
		n, err := d.hid.Write(out)
		if err == nil || attempt == maxInterruptRetries || !isEINTR(err) {
			return n, err
		}
	}
}

// readReport reads one HID response, retrying when a signal interrupts the
// read. The response is still pending after an interruption, so the retry
// picks it up. Each attempt gets a fresh timeout.
func (d *device) readReport(in []byte) (int, error) {
	for attempt := 0; ; attempt++ {
		n, err := d.hid.ReadWithTimeout(in, responseTimeout)
		if err == nil || attempt == maxInterruptRetries || !isEINTR(err) {
			return n, err
		}
	}
}

// command sends one 32-byte QMK Raw HID packet and waits for the 32-byte
// response.
//
// HIDAPI requires an additional leading report-ID byte when writing.
// QMK's Raw HID report has no report ID, so that byte is zero.
func (d *device) command(payload ...byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("empty HID command")
	}

	if len(payload) > packetSize {
		return nil, fmt.Errorf(
			"HID command too large: %d bytes; maximum is %d",
			len(payload),
			packetSize,
		)
	}

	// Byte 0 is HID report ID 0.
	// Bytes 1..32 are the actual QMK Raw HID packet.
	out := make([]byte, packetSize+1)
	copy(out[1:], payload)

	n, err := d.writeReport(out)
	if err != nil {
		return nil, fmt.Errorf("writing HID report: %w", err)
	}

	if n != len(out) {
		return nil, fmt.Errorf(
			"short HID write: wrote %d bytes, expected %d",
			n,
			len(out),
		)
	}

	in := make([]byte, packetSize)

	n, err = d.readReport(in)
	if err != nil {
		if errors.Is(err, hid.ErrTimeout) {
			return nil, errors.New("timeout waiting for HID response")
		}

		return nil, fmt.Errorf("reading HID response: %w", err)
	}

	resp := in[:n]

	if resp[0] == 0xff {
		return nil, fmt.Errorf("device reported VIA command 0x%02x as unhandled", payload[0])
	}

	if resp[0] != payload[0] {
		return nil, fmt.Errorf(
			"unexpected VIA response command: got 0x%02x, expected 0x%02x",
			resp[0],
			payload[0],
		)
	}

	return resp, nil
}

func (d *device) protocolVersion() (uint16, error) {
	resp, err := d.command(cmdGetProtocolVersion)
	if err != nil {
		return 0, err
	}

	if len(resp) < 3 {
		return 0, fmt.Errorf("short VIA protocol-version response: %d bytes", len(resp))
	}

	return uint16(resp[1])<<8 | uint16(resp[2]), nil
}

func (d *device) checkProtocol() error {
	version, err := d.protocolVersion()
	if err != nil {
		return &codeError{code: exitHIDFailure, err: err}
	}

	if version != expectedVIAProtocol {
		return &codeError{
			code: exitProtocolMismatch,
			err: fmt.Errorf(
				"unsupported VIA protocol version %d; expected %d",
				version,
				expectedVIAProtocol,
			),
		}
	}

	return nil
}

// Vial-QMK's VIA protocol v9 RGBLIGHT packet format is:
//
//	Set: [0x07, value_id, value_data...]
//	Get: [0x08, value_id, ...]
//
// RGBLIGHT value IDs are 0x80..0x83.
func (d *device) setRGB(valueID byte, values ...byte) error {
	payload := make([]byte, 0, 2+len(values))
	payload = append(payload, cmdLightingSetValue, valueID)
	payload = append(payload, values...)

	resp, err := d.command(payload...)
	if err != nil {
		return err
	}

	if len(resp) < 2 {
		return fmt.Errorf("short RGBLIGHT set response: %d bytes", len(resp))
	}

	if resp[1] != valueID {
		return fmt.Errorf(
			"unexpected RGBLIGHT value ID in response: got 0x%02x, expected 0x%02x",
			resp[1],
			valueID,
		)
	}

	return nil
}

func (d *device) getRGB(valueID byte, valueBytes int) ([]byte, error) {
	resp, err := d.command(cmdLightingGetValue, valueID)
	if err != nil {
		return nil, err
	}

	if len(resp) < 2+valueBytes {
		return nil, fmt.Errorf(
			"short RGBLIGHT get response for value 0x%02x: got %d bytes",
			valueID,
			len(resp),
		)
	}

	if resp[1] != valueID {
		return nil, fmt.Errorf(
			"unexpected RGBLIGHT value ID in response: got 0x%02x, expected 0x%02x",
			resp[1],
			valueID,
		)
	}

	result := make([]byte, valueBytes)
	copy(result, resp[2:2+valueBytes])

	return result, nil
}

func (d *device) setBrightness(value uint8) error {
	return d.setRGB(rgbBrightness, value)
}

func (d *device) getBrightness() (uint8, error) {
	value, err := d.getRGB(rgbBrightness, 1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (d *device) setEffect(value uint8) error {
	return d.setRGB(rgbEffect, value)
}

func (d *device) getEffect() (uint8, error) {
	value, err := d.getRGB(rgbEffect, 1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (d *device) setSpeed(value uint8) error {
	return d.setRGB(rgbSpeed, value)
}

func (d *device) getSpeed() (uint8, error) {
	value, err := d.getRGB(rgbSpeed, 1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (d *device) setColor(value hsv) error {
	return d.setRGB(rgbColor, value.h, value.s)
}

func (d *device) getColor() (hsv, error) {
	value, err := d.getRGB(rgbColor, 2)
	if err != nil {
		return hsv{}, err
	}

	return hsv{
		h: value[0],
		s: value[1],
	}, nil
}

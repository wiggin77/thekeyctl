package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
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

	// With only RGBLIGHT_EFFECT_BREATHING compiled:
	//
	//   0 = off
	//   1 = static
	//   2 = breathing 0
	//   3 = breathing 1
	//   4 = breathing 2
	//   5 = breathing 3
	effectOff       byte = 0
	effectStatic    byte = 1
	effectBreathing byte = 2
)

type hsv struct {
	h uint8
	s uint8
}

var colors = map[string]hsv{
	"red":    {0, 255},
	"amber":  {21, 255},
	"orange": {21, 255},
	"yellow": {43, 255},
	"green":  {85, 255},
	"cyan":   {128, 255},
	"blue":   {170, 255},
	"purple": {191, 255},
	"pink":   {213, 255},
	"white":  {0, 0},
}

type device struct {
	hid *hid.Device
}

func openDevice() (*device, error) {
	var path string

	err := hid.Enumerate(vendorID, productID, func(info *hid.DeviceInfo) error {
		if path != "" {
			return nil
		}

		if info.UsagePage == rawUsagePage && info.Usage == rawUsage {
			path = info.Path
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enumerating HID devices: %w", err)
	}

	if path == "" {
		return nil, fmt.Errorf(
			"The Key V2 Vial HID interface not found (VID=%04x PID=%04x usage=%04x:%04x)",
			vendorID,
			productID,
			rawUsagePage,
			rawUsage,
		)
	}

	d, err := hid.OpenPath(path)
	if err != nil {
		return nil, fmt.Errorf("opening HID device %q: %w", path, err)
	}

	return &device{hid: d}, nil
}

func (d *device) close() error {
	return d.hid.Close()
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

	n, err := d.hid.Write(out)
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

	n, err = d.hid.ReadWithTimeout(in, time.Second)
	if err != nil {
		return nil, fmt.Errorf("reading HID response: %w", err)
	}

	if n == 0 {
		return nil, errors.New("timeout waiting for HID response")
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
		return err
	}

	if version != expectedVIAProtocol {
		return fmt.Errorf(
			"unsupported VIA protocol version %d; expected %d",
			version,
			expectedVIAProtocol,
		)
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

func parseColor(name string) (hsv, error) {
	value, ok := colors[name]
	if !ok {
		return hsv{}, fmt.Errorf("unknown color %q", name)
	}

	return value, nil
}

func uint8Arg(name string, value int) (uint8, error) {
	if value < 0 || value > 255 {
		return 0, fmt.Errorf("%s must be between 0 and 255", name)
	}

	return uint8(value), nil
}

func ledOff(d *device) error {
	return d.setEffect(effectOff)
}

func ledSolid(d *device, args []string) error {
	fs := flag.NewFlagSet("led solid", flag.ContinueOnError)

	colorName := fs.String("color", "white", "LED color")
	brightnessArg := fs.Int("brightness", 64, "brightness, 0-255")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	color, err := parseColor(*colorName)
	if err != nil {
		return err
	}

	brightness, err := uint8Arg("brightness", *brightnessArg)
	if err != nil {
		return err
	}

	// Set HSV before enabling the LEDs so the transition from off does not
	// briefly show the previous color.
	if err := d.setColor(color); err != nil {
		return fmt.Errorf("setting color: %w", err)
	}

	if err := d.setBrightness(brightness); err != nil {
		return fmt.Errorf("setting brightness: %w", err)
	}

	if err := d.setEffect(effectStatic); err != nil {
		return fmt.Errorf("enabling static lighting: %w", err)
	}

	return nil
}

func ledAlert(d *device, args []string) error {
	fs := flag.NewFlagSet("led alert", flag.ContinueOnError)

	colorName := fs.String("color", "amber", "alert color")
	brightnessArg := fs.Int("brightness", 80, "brightness, 0-255")
	rateArg := fs.Int("rate", 1, "breathing rate, 1-4")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	color, err := parseColor(*colorName)
	if err != nil {
		return err
	}

	brightness, err := uint8Arg("brightness", *brightnessArg)
	if err != nil {
		return err
	}

	if *rateArg < 1 || *rateArg > 4 {
		return errors.New("rate must be between 1 and 4")
	}

	// QMK assigns four consecutive modes to the breathing effect.
	//
	// rate=1 -> mode 2
	// rate=2 -> mode 3
	// rate=3 -> mode 4
	// rate=4 -> mode 5
	effect := effectBreathing + byte(*rateArg-1)

	if err := d.setColor(color); err != nil {
		return fmt.Errorf("setting alert color: %w", err)
	}

	if err := d.setBrightness(brightness); err != nil {
		return fmt.Errorf("setting alert brightness: %w", err)
	}

	if err := d.setEffect(effect); err != nil {
		return fmt.Errorf("enabling breathing effect: %w", err)
	}

	return nil
}

func status(d *device) error {
	protocol, err := d.protocolVersion()
	if err != nil {
		return fmt.Errorf("getting VIA protocol version: %w", err)
	}

	effect, err := d.getEffect()
	if err != nil {
		return fmt.Errorf("getting effect: %w", err)
	}

	brightness, err := d.getBrightness()
	if err != nil {
		return fmt.Errorf("getting brightness: %w", err)
	}

	color, err := d.getColor()
	if err != nil {
		return fmt.Errorf("getting color: %w", err)
	}

	speed, err := d.getSpeed()
	if err != nil {
		return fmt.Errorf("getting speed: %w", err)
	}

	fmt.Printf("protocol:   %d\n", protocol)
	fmt.Printf("effect:     %d\n", effect)
	fmt.Printf("brightness: %d\n", brightness)
	fmt.Printf("hue:        %d\n", color.h)
	fmt.Printf("saturation: %d\n", color.s)
	fmt.Printf("speed:      %d\n", speed)

	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  thekeyctl status

  thekeyctl led off
  thekeyctl led solid [--color COLOR] [--brightness 0-255]
  thekeyctl led alert [--color COLOR] [--brightness 0-255] [--rate 1-4]

Colors:
  red
  amber
  orange
  yellow
  green
  cyan
  blue
  purple
  pink
  white

Examples:
  thekeyctl led off
  thekeyctl led solid --color blue --brightness 40
  thekeyctl led alert
  thekeyctl led alert --color red --brightness 100 --rate 2
`)
}

func run() error {
	if len(os.Args) < 2 {
		usage()
		return errors.New("command required")
	}

	if err := hid.Init(); err != nil {
		return fmt.Errorf("initializing HIDAPI: %w", err)
	}
	defer func() {
		_ = hid.Exit()
	}()

	d, err := openDevice()
	if err != nil {
		return err
	}
	defer func() {
		_ = d.close()
	}()

	if err := d.checkProtocol(); err != nil {
		return err
	}

	switch os.Args[1] {
	case "status":
		if len(os.Args) != 2 {
			usage()
			return errors.New("status takes no arguments")
		}
		return status(d)

	case "led":
		if len(os.Args) < 3 {
			usage()
			return errors.New("LED command required")
		}

		switch os.Args[2] {
		case "off":
			if len(os.Args) != 3 {
				usage()
				return errors.New("led off takes no arguments")
			}
			return ledOff(d)

		case "solid":
			return ledSolid(d, os.Args[3:])

		case "alert":
			return ledAlert(d, os.Args[3:])

		default:
			usage()
			return fmt.Errorf("unknown LED command %q", os.Args[2])
		}

	case "help", "-h", "--help":
		usage()
		return nil

	default:
		usage()
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "thekeyctl: %v\n", err)
		os.Exit(1)
	}
}

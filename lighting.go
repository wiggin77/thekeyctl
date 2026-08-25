package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
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

func parseColor(name string) (hsv, error) {
	value, ok := colors[name]
	if !ok {
		return hsv{}, fmt.Errorf("unknown color %q", name)
	}

	return value, nil
}

type deviceInfoData struct {
	Product           string `json:"product"`
	VID               string `json:"vid"`
	PID               string `json:"pid"`
	HIDPath           string `json:"hid_path"`
	VIAProtocol       uint16 `json:"via_protocol"`
	RGBLIGHTAvailable bool   `json:"rgblight_available"`
}

func gatherDeviceInfo(d *device) (*deviceInfoData, error) {
	protocol, err := d.protocolVersion()
	if err != nil {
		return nil, fmt.Errorf("getting VIA protocol version: %w", err)
	}

	rgblightAvailable := true
	if _, err := d.getRGB(rgbBrightness, 1); err != nil {
		if strings.Contains(err.Error(), "unhandled") {
			rgblightAvailable = false
		} else {
			return nil, fmt.Errorf("checking RGBLIGHT: %w", err)
		}
	}

	name := d.product
	if name == "" {
		name = "unknown"
	}

	return &deviceInfoData{
		Product:           name,
		VID:               fmt.Sprintf("%04x", vendorID),
		PID:               fmt.Sprintf("%04x", productID),
		HIDPath:           d.path,
		VIAProtocol:       protocol,
		RGBLIGHTAvailable: rgblightAvailable,
	}, nil
}

func deviceInfo(d *device) error {
	data, err := gatherDeviceInfo(d)
	if err != nil {
		return err
	}

	rgblightStr := "available"
	if !data.RGBLIGHTAvailable {
		rgblightStr = "not available"
	}

	fmt.Fprintf(stdout, "Device:       %s\n", data.Product)
	fmt.Fprintf(stdout, "VID:PID:      %s:%s\n", data.VID, data.PID)
	fmt.Fprintf(stdout, "HID path:     %s\n", data.HIDPath)
	fmt.Fprintf(stdout, "VIA protocol: %d\n", data.VIAProtocol)
	fmt.Fprintf(stdout, "RGBLIGHT:     %s\n", rgblightStr)

	return nil
}

func deviceInfoJSON(d *device) error {
	data, err := gatherDeviceInfo(d)
	if err != nil {
		return err
	}

	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding device info as JSON: %w", err)
	}

	fmt.Fprintln(stdout, string(out))

	return nil
}

func ledOff(d *device) error {
	return d.setEffect(effectOff)
}

// applyLighting turns the LEDs on with the requested color, brightness and
// effect.
//
// The write order matters, because QMK's RGBLIGHT drops writes it cannot
// apply to the current state:
//
//   - While RGBLIGHT is disabled, every write is ignored, including the effect
//     mode itself. A nonzero effect write only enables RGBLIGHT, which comes
//     up in static mode still holding the previous color.
//   - While a breathing effect is running, brightness writes are ignored.
//
// So the LEDs are enabled in static mode, fully configured there, and only
// then switched to the requested effect. Brightness is dropped to zero first
// so the previous color is not visible while the new settings are applied.
func applyLighting(d *device, color hsv, brightness uint8, effect byte) error {
	if err := d.setEffect(effectStatic); err != nil {
		return fmt.Errorf("enabling lighting: %w", err)
	}

	if err := d.setBrightness(0); err != nil {
		return fmt.Errorf("blanking LEDs: %w", err)
	}

	if err := d.setColor(color); err != nil {
		return fmt.Errorf("setting color: %w", err)
	}

	if err := d.setBrightness(brightness); err != nil {
		return fmt.Errorf("setting brightness: %w", err)
	}

	if effect != effectStatic {
		if err := d.setEffect(effect); err != nil {
			return fmt.Errorf("setting effect: %w", err)
		}
	}

	return nil
}

type statusData struct {
	ProtocolVersion uint16 `json:"protocol_version"`
	Effect          uint8  `json:"effect"`
	Brightness      uint8  `json:"brightness"`
	Hue             uint8  `json:"hue"`
	Saturation      uint8  `json:"saturation"`
	Speed           uint8  `json:"speed"`
}

func gatherStatus(d *device) (*statusData, error) {
	protocol, err := d.protocolVersion()
	if err != nil {
		return nil, fmt.Errorf("getting VIA protocol version: %w", err)
	}

	effect, err := d.getEffect()
	if err != nil {
		return nil, fmt.Errorf("getting effect: %w", err)
	}

	brightness, err := d.getBrightness()
	if err != nil {
		return nil, fmt.Errorf("getting brightness: %w", err)
	}

	color, err := d.getColor()
	if err != nil {
		return nil, fmt.Errorf("getting color: %w", err)
	}

	speed, err := d.getSpeed()
	if err != nil {
		return nil, fmt.Errorf("getting speed: %w", err)
	}

	return &statusData{
		ProtocolVersion: protocol,
		Effect:          effect,
		Brightness:      brightness,
		Hue:             color.h,
		Saturation:      color.s,
		Speed:           speed,
	}, nil
}

func status(d *device) error {
	data, err := gatherStatus(d)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "protocol:   %d\n", data.ProtocolVersion)
	fmt.Fprintf(stdout, "effect:     %d\n", data.Effect)
	fmt.Fprintf(stdout, "brightness: %d\n", data.Brightness)
	fmt.Fprintf(stdout, "hue:        %d\n", data.Hue)
	fmt.Fprintf(stdout, "saturation: %d\n", data.Saturation)
	fmt.Fprintf(stdout, "speed:      %d\n", data.Speed)

	return nil
}

func statusJSON(d *device) error {
	data, err := gatherStatus(d)
	if err != nil {
		return err
	}

	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding status as JSON: %w", err)
	}

	fmt.Fprintln(stdout, string(out))

	return nil
}

package main

import "fmt"

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

	fmt.Fprintf(stdout, "protocol:   %d\n", protocol)
	fmt.Fprintf(stdout, "effect:     %d\n", effect)
	fmt.Fprintf(stdout, "brightness: %d\n", brightness)
	fmt.Fprintf(stdout, "hue:        %d\n", color.h)
	fmt.Fprintf(stdout, "saturation: %d\n", color.s)
	fmt.Fprintf(stdout, "speed:      %d\n", speed)

	return nil
}

# thekeyctl

`thekeyctl` is a small Go CLI for controlling the RGB LEDs on the **Drop The Key V2** macropad.

It is intended for using the macropad as an application-attention indicator. An application can start a breathing LED alert, and pressing any of the three physical keys acknowledges the alert by turning the LEDs off while still sending the configured keypress.

The project assumes the macropad is running the custom **Vial + QMK RGBLIGHT** firmware described below.

## Features

- Control The Key V2 LEDs from the command line
- Solid-color lighting
- Breathing alert effect
- Configurable color, brightness, and breathing rate
- Turn LEDs off
- Query the current lighting state
- No background daemon required
- Key mappings are stored in the keyboard by Vial
- Pressing any macropad key automatically clears an active LED alert
- Lighting changes made by `thekeyctl` are transient and do not write to EEPROM

## Supported hardware

This project targets:

- **Drop Inc. The Key V2**
- USB VID/PID: `359b:000e`
- MCU: ATmega32U4
- Bootloader: Atmel DFU
- QMK target: `drop/thekey/v2`

After flashing the custom firmware, `lsusb` should show:

```text
ID 359b:000e Drop Inc. The Key V2
```

## CLI usage

```text
thekeyctl status

thekeyctl led off
thekeyctl led solid [--color COLOR] [--brightness 0-255]
thekeyctl led alert [--color COLOR] [--brightness 0-255] [--rate 1-4]
```

Supported named colors:

```text
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
```

Examples:

```bash
thekeyctl status

thekeyctl led solid --color blue --brightness 40

thekeyctl led alert

thekeyctl led alert --color red --brightness 100 --rate 2

thekeyctl led off
```

The default alert is an amber breathing effect.

When an alert is active, pressing any of the three physical keys turns the LEDs off and then processes the key normally.

## Building the CLI

### Ubuntu 24.04 prerequisites

```bash
sudo apt install build-essential libudev-dev
```

The CLI uses [`github.com/sstallion/go-hid`](https://github.com/sstallion/go-hid) to communicate with the Vial/VIA Raw HID interface.

From the repository:

```bash
go mod tidy
go build -o thekeyctl .
```

Test it:

```bash
./thekeyctl status
```

Install it system-wide:

```bash
sudo install -m 0755 ./thekeyctl /usr/local/bin/thekeyctl
```

After that:

```bash
thekeyctl led alert
thekeyctl led off
```

## Linux HID permissions

The CLI needs access to the Vial Raw HID interface.

Create a udev rule specifically for The Key V2:

```bash
sudo tee /etc/udev/rules.d/59-thekeyctl.rules >/dev/null <<'EOF'
KERNEL=="hidraw*", SUBSYSTEM=="hidraw", ATTRS{idVendor}=="359b", ATTRS{idProduct}=="000e", MODE="0660", TAG+="uaccess"
EOF
```

Reload the rules:

```bash
sudo udevadm control --reload-rules
```

Then unplug and reconnect the macropad.

## Custom firmware

The stock firmware originally found on this device identified itself as:

```text
feed:6060 qmkbuilder keyboard
```

That firmware was QMK-based but was not Vial-enabled, so runtime key and RGB configuration were not available.

The custom firmware described here is based on the upstream Vial-QMK target:

```text
drop/thekey/v2
```

It adds:

- Vial runtime key mapping
- QMK RGBLIGHT support
- Static RGB lighting
- QMK breathing effect
- LEDs off by default
- Automatic clearing of active lighting when any physical key is pressed

Once this firmware is flashed, normal keymap and lighting configuration does not require reflashing.

## Building Vial-QMK firmware

### 1. Install QMK tooling

Install the required QMK tooling using the current QMK installation instructions for your Linux distribution.

Verify that the CLI is available:

```bash
qmk --version
```

### 2. Clone Vial-QMK

```bash
mkdir -p ~/Development
cd ~/Development

git clone --depth 1 --branch vial --recurse-submodules --shallow-submodules \
    https://github.com/vial-kb/vial-qmk.git

cd vial-qmk
```

Point the QMK CLI at this checkout:

```bash
qmk config user.qmk_home="$PWD"
```

Optionally create a branch for the custom firmware:

```bash
git switch -c thekey-v2-vial-rgb
```

The files modified below are under:

```text
keyboards/drop/thekey/v2/keymaps/vial/
```

### 3. Enable Vial and RGBLIGHT

Set `keyboards/drop/thekey/v2/keymaps/vial/rules.mk` to:

```make
VIA_ENABLE       = yes
VIAL_ENABLE      = yes
LTO_ENABLE       = yes

RGBLIGHT_ENABLE  = yes

# Save flash/RAM on the ATmega32U4.
QMK_SETTINGS        = no
MOUSEKEY_ENABLE     = no
TAP_DANCE_ENABLE    = no
COMBO_ENABLE        = no
KEY_OVERRIDE_ENABLE = no
CAPS_WORD_ENABLE    = no
LAYER_LOCK_ENABLE   = no
REPEAT_KEY_ENABLE   = no
```

### 4. Expose RGBLIGHT through Vial

In:

```text
keyboards/drop/thekey/v2/keymaps/vial/vial.json
```

change:

```json
"lighting": "none"
```

to:

```json
"lighting": "qmk_rgblight"
```

### 5. Configure the RGB defaults and effects

Add the following to:

```text
keyboards/drop/thekey/v2/keymaps/vial/config.h
```

```c
#define RGBLIGHT_DEFAULT_ON false
#define RGBLIGHT_DEFAULT_VAL 64

// Keep breathing; remove the other animation effects.
#undef RGBLIGHT_EFFECT_ALTERNATING
#undef RGBLIGHT_EFFECT_CHRISTMAS
#undef RGBLIGHT_EFFECT_KNIGHT
#undef RGBLIGHT_EFFECT_RAINBOW_MOOD
#undef RGBLIGHT_EFFECT_RAINBOW_SWIRL
#undef RGBLIGHT_EFFECT_RGB_TEST
#undef RGBLIGHT_EFFECT_SNAKE
#undef RGBLIGHT_EFFECT_STATIC_GRADIENT
#undef RGBLIGHT_EFFECT_TWINKLE
```

Static lighting is always available. The breathing effect remains enabled.

### 6. Turn off an active alert when a key is pressed

Replace:

```text
keyboards/drop/thekey/v2/keymaps/vial/keymap.c
```

with:

```c
// Copyright 2023 Massdrop, Inc.
// SPDX-License-Identifier: GPL-2.0-or-later

#include QMK_KEYBOARD_H

const uint16_t PROGMEM keymaps[][MATRIX_ROWS][MATRIX_COLS] = {
    [0] = LAYOUT(KC_LCTL, KC_C, KC_V)
};

bool process_record_user(uint16_t keycode, keyrecord_t *record) {
    // A physical key press acknowledges any active LED alert.
    //
    // Use the no-EEPROM version because alerts are transient state.
    // The key press itself is still processed normally because we
    // return true.
    if (record->event.pressed && rgblight_is_enabled()) {
        rgblight_disable_noeeprom();
    }

    return true;
}
```

The compiled `KC_LCTL`, `KC_C`, and `KC_V` values are only defaults. Once Vial is running, the actual key assignments can be changed dynamically and stored in the keyboard.

## Compile the firmware

Compile without flashing first:

```bash
qmk compile -kb drop/thekey/v2 -km vial
```

A known-good build of this configuration produced approximately:

```text
The firmware size is fine - 17286/28672 (60%, 11386 bytes free)
```

The exact number can vary slightly with QMK/Vial revisions.

The resulting firmware is:

```text
drop_thekey_v2_vial.hex
```

## Flashing the firmware

The Key V2 uses the Atmel DFU bootloader.

Start the flash process:

```bash
qmk flash -kb drop/thekey/v2 -km vial
```

When QMK waits for the bootloader, press the small physical reset button marked `S1` on the PCB.

If necessary:

1. Unplug the macropad.
2. Hold `S1`.
3. Plug the USB cable in.
4. Wait about a second.
5. Release `S1`.

In DFU mode, `lsusb` should normally show an ATmega32U4 DFU device such as:

```text
03eb:2ff4 Atmel Corp. ATmega32U4 DFU
```

A successful flash looks similar to:

```text
Erasing flash...  Success
Programming ...   Success
Reading ...       Success
Validating...     Success
```

After the firmware restarts:

```bash
lsusb | grep -Ei '359b|the.?key'
```

should show:

```text
359b:000e Drop Inc. The Key V2
```

The LEDs should initially be off.

## Configuring the keys with Vial

After flashing, use Vial to configure the three keys.

The browser version works in Chromium-based browsers:

- https://vial.rocks/

Authorize **Drop Inc. The Key V2**, then select each physical key and assign the desired keycode.

Vial writes keymap changes to the keyboard immediately. There is no separate Save operation.

For example, the three keys can be mapped to:

```text
Left   -> 1
Middle -> 2
Right  -> 3
```

The mappings survive unplugging, rebooting, and moving the keyboard to another computer.

Because mappings are stored in the keyboard, an OS-level remapper such as `input-remapper` is not required.

## How the alert workflow works

A typical application integration is:

```text
Application needs user input
        |
        v
thekeyctl led alert
        |
        v
Macropad LEDs breathe
        |
        v
User presses one of the three keys
        |
        +--> Firmware turns the LEDs off
        |
        +--> Configured keypress is sent to the application
```

No daemon is required and the CLI does not need to stay running after starting an alert.

Example from an application or shell script:

```bash
thekeyctl led alert

# Wait for / perform other application logic.
# The user's physical keypress acknowledges the alert.

thekeyctl led off
```

Calling `led off` explicitly is still useful when an application wants to cancel an outstanding alert programmatically.

## Protocol notes

`thekeyctl` talks directly to the Vial/VIA Raw HID interface exposed by the custom firmware.

For the current Vial-QMK firmware used by this project:

- Raw HID usage page: `0xff60`
- Raw HID usage: `0x0061`
- HID payload size: 32 bytes
- VIA protocol version: 9
- RGBLIGHT value IDs:
  - brightness: `0x80`
  - effect: `0x81`
  - speed: `0x82`
  - color: `0x83`

Lighting operations use QMK's non-EEPROM RGBLIGHT functions, so application alerts do not continually write transient state to the keyboard's EEPROM.

## Development workflow

After making CLI changes:

```bash
gofmt -w main.go
go test ./...
go build -o thekeyctl .
```

After making firmware changes:

```bash
cd ~/Development/vial-qmk
qmk compile -kb drop/thekey/v2 -km vial
```

Only flash after confirming the build succeeds and fits within the ATmega32U4 firmware size limit.

## Recovery

The hardware DFU bootloader remains available independently of the application firmware.

If a future firmware build fails to boot correctly, use the physical `S1` reset button to enter Atmel DFU mode and flash a known-good image again.


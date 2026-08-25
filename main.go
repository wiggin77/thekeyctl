package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	hid "github.com/sstallion/go-hid"
)

// Process exit codes. These are stable across releases and suitable for use
// by calling scripts and applications.
const (
	exitSuccess          = 0 // command completed successfully
	exitUsage            = 1 // invalid arguments or usage error
	exitDeviceNotFound   = 2 // macropad not connected or Vial HID interface not found
	exitAccessDenied     = 3 // permission denied opening the HID device (check udev rules)
	exitProtocolMismatch = 4 // VIA protocol version not supported by this build
	exitHIDFailure       = 5 // HID communication error (I/O failure, timeout, etc.)
)

// codeError is an error that carries a stable process exit code.
type codeError struct {
	code int
	err  error
}

func (e *codeError) Error() string { return e.err.Error() }
func (e *codeError) Unwrap() error { return e.err }

// exitCode returns the exit code embedded in err, or exitUsage if err carries
// none (which should not happen in normal operation).
func exitCode(err error) int {
	var ce *codeError
	if errors.As(err, &ce) {
		return ce.code
	}

	return exitUsage
}

// stdout and stderr are the program's output streams. They are variables so
// tests can capture what the CLI prints.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// usage prints the top-level usage to stderr, for the paths where it
// accompanies an error message.
func usage() {
	fprintUsage(stderr)
}

// helpUsage prints the top-level usage to stdout, for an explicit help
// request, so that it can be piped or redirected.
func helpUsage() {
	fprintUsage(stdout)
}

func fprintUsage(w io.Writer) {
	fmt.Fprintf(w, `Usage:
  thekeyctl device info [--json]
  thekeyctl status [--json]

  thekeyctl led off
  thekeyctl led solid [--color COLOR] [--brightness 0-255]
  thekeyctl led alert [--color COLOR] [--brightness 0-255] [--rate 1-4]

Colors:
  red, amber, orange, yellow, green, cyan, blue, purple, pink, white

Examples:
  thekeyctl device info
  thekeyctl device info --json
  thekeyctl status
  thekeyctl status --json
  thekeyctl led off
  thekeyctl led solid --color blue --brightness 40
  thekeyctl led alert
  thekeyctl led alert --color red --brightness 100 --rate 2
`)
}

func uint8Arg(name string, value int) (uint8, error) {
	if value < 0 || value > 255 {
		return 0, fmt.Errorf("%s must be between 0 and 255", name)
	}

	return uint8(value), nil
}

// parseFlags parses a subcommand's flags and rejects leftover positional
// arguments.
//
// The flag package's own output is suppressed during parsing so that a bad
// flag is reported once, by main, instead of twice. The flag defaults are then
// printed to stdout for an explicit -h and to stderr when they accompany an
// error.
//
// It returns done=true when the flags were fully handled by printing help.
func parseFlags(fs *flag.FlagSet, args []string) (bool, error) {
	fs.SetOutput(io.Discard)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stdout)
			fs.Usage()

			return true, nil
		}

		fs.SetOutput(stderr)
		fs.Usage()

		return false, err
	}

	if fs.NArg() != 0 {
		fs.SetOutput(stderr)
		fs.Usage()

		return false, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	return false, nil
}

// parseStatus validates the "status" flags without touching the device.
//
// A nil handler with a nil error means the flags were fully handled, such as
// when --help was requested.
func parseStatus(args []string) (handler, error) {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "output as JSON")

	done, err := parseFlags(fs, args)
	if err != nil {
		return nil, err
	}

	if done {
		return nil, nil
	}

	if *jsonOut {
		return statusJSON, nil
	}

	return status, nil
}

// parseDeviceInfo validates the "device info" flags without touching the device.
//
// A nil handler with a nil error means the flags were fully handled, such as
// when --help was requested.
func parseDeviceInfo(args []string) (handler, error) {
	fs := flag.NewFlagSet("device info", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "output as JSON")

	done, err := parseFlags(fs, args)
	if err != nil {
		return nil, err
	}

	if done {
		return nil, nil
	}

	if *jsonOut {
		return deviceInfoJSON, nil
	}

	return deviceInfo, nil
}

// parseLEDSolid validates the "led solid" flags without touching the device.
//
// A nil handler with a nil error means the flags were fully handled, such as
// when --help was requested.
func parseLEDSolid(args []string) (handler, error) {
	fs := flag.NewFlagSet("led solid", flag.ContinueOnError)

	colorName := fs.String("color", "white", "LED color")
	brightnessArg := fs.Int("brightness", 64, "brightness, 0-255")

	done, err := parseFlags(fs, args)
	if err != nil {
		return nil, err
	}

	if done {
		return nil, nil
	}

	color, err := parseColor(*colorName)
	if err != nil {
		return nil, err
	}

	brightness, err := uint8Arg("brightness", *brightnessArg)
	if err != nil {
		return nil, err
	}

	return func(d *device) error {
		return applyLighting(d, color, brightness, effectStatic)
	}, nil
}

// parseLEDAlert validates the "led alert" flags without touching the device.
//
// A nil handler with a nil error means the flags were fully handled, such as
// when --help was requested.
func parseLEDAlert(args []string) (handler, error) {
	fs := flag.NewFlagSet("led alert", flag.ContinueOnError)

	colorName := fs.String("color", "amber", "alert color")
	brightnessArg := fs.Int("brightness", 80, "brightness, 0-255")
	rateArg := fs.Int("rate", 1, "breathing rate, 1-4")

	done, err := parseFlags(fs, args)
	if err != nil {
		return nil, err
	}

	if done {
		return nil, nil
	}

	color, err := parseColor(*colorName)
	if err != nil {
		return nil, err
	}

	brightness, err := uint8Arg("brightness", *brightnessArg)
	if err != nil {
		return nil, err
	}

	if *rateArg < 1 || *rateArg > 4 {
		return nil, errors.New("rate must be between 1 and 4")
	}

	// QMK assigns four consecutive modes to the breathing effect.
	//
	// rate=1 -> mode 2
	// rate=2 -> mode 3
	// rate=3 -> mode 4
	// rate=4 -> mode 5
	effect := effectBreathing + byte(*rateArg-1)

	return func(d *device) error {
		return applyLighting(d, color, brightness, effect)
	}, nil
}

// handler performs a command against an open, protocol-checked device.
type handler func(d *device) error

// parseCommand validates the command line without opening the device, so that
// help and argument errors work with no macropad attached.
//
// A nil handler with a nil error means the command was fully handled by
// printing usage.
func parseCommand(args []string) (handler, error) {
	if len(args) == 0 {
		usage()
		return nil, errors.New("command required")
	}

	switch args[0] {
	case "device":
		if len(args) < 2 {
			usage()
			return nil, errors.New("device command required")
		}

		switch args[1] {
		case "info":
			return parseDeviceInfo(args[2:])

		default:
			usage()
			return nil, fmt.Errorf("unknown device command %q", args[1])
		}

	case "status":
		return parseStatus(args[1:])

	case "led":
		if len(args) < 2 {
			usage()
			return nil, errors.New("LED command required")
		}

		switch args[1] {
		case "off":
			if len(args) != 2 {
				usage()
				return nil, errors.New("led off takes no arguments")
			}
			return ledOff, nil

		case "solid":
			return parseLEDSolid(args[2:])

		case "alert":
			return parseLEDAlert(args[2:])

		default:
			usage()
			return nil, fmt.Errorf("unknown LED command %q", args[1])
		}

	case "help", "-h", "--help":
		helpUsage()
		return nil, nil

	default:
		usage()
		return nil, fmt.Errorf("unknown command %q", args[0])
	}
}

func run() error {
	h, err := parseCommand(os.Args[1:])
	if err != nil {
		return &codeError{code: exitUsage, err: err}
	}

	if h == nil {
		return nil
	}

	if err := hid.Init(); err != nil {
		return &codeError{
			code: exitHIDFailure,
			err:  fmt.Errorf("initializing HIDAPI: %w", err),
		}
	}
	defer func() {
		_ = hid.Exit()
	}()

	d, err := openDevice()
	if err != nil {
		return err // already a *codeError from openDevice
	}
	defer func() {
		_ = d.close()
	}()

	if err := d.checkProtocol(); err != nil {
		return err // already a *codeError from checkProtocol
	}

	if err := h(d); err != nil {
		return &codeError{code: exitHIDFailure, err: err}
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(stderr, "thekeyctl: %v\n", err)
		os.Exit(exitCode(err))
	}
}

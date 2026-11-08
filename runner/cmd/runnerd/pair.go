package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/mdp/qrterminal/v3"

	"relay/runner/internal/uplink"
)

// pollEvery is how often `pair` asks the hub whether the phone confirmed.
var pollEvery = 2 * time.Second

// openURL opens a link in the default browser. A var so tests don't launch one.
var openURL = func(u string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		return exec.Command("open", u).Start()
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return fmt.Errorf("no display (DISPLAY/WAYLAND_DISPLAY unset)")
		}
		return exec.Command("xdg-open", u).Start()
	}
}

// runPair implements `runnerd pair [--hub URL] [--name NAME] [--code CODE] [--open]`
// and saves hub.json in relayHome (next to key.txt). See shared/HUB.md.
//   - no --code (recommended): QR pairing - print a QR of the hub's claim
//     link, the phone scans it and taps Confirm, we poll until it does.
//   - --code: desktop alternative - redeem a code shown in the browser.
//
// Hub: --hub, else $RELAY_HUB, else uplink.DefaultHub.
func runPair(args []string, relayHome string, out io.Writer) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(out)
	hub := fs.String("hub", "", "hub base URL (default $RELAY_HUB, else "+uplink.DefaultHub+"; https required unless localhost)")
	code := fs.String("code", "", "8-char pairing code from the browser (desktop alternative to the QR)")
	open := fs.Bool("open", false, "QR mode: also open the claim link in this machine's default browser (ignored with --code)")
	name := fs.String("name", "", "device name shown in the browser (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hub == "" {
		*hub = os.Getenv("RELAY_HUB")
	}
	if *hub == "" {
		*hub = uplink.DefaultHub
	}
	if *name == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "runner"
		}
		*name = h
	}
	var cfg *uplink.Config
	var err error
	if *code != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cfg, err = uplink.Pair(ctx, nil, *hub, *code, *name)
		cancel()
	} else {
		cfg, err = pairByQR(*hub, *name, *open, out)
	}
	if err != nil {
		return err
	}
	if err := uplink.Save(relayHome, *cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "Connected to %s as %s - the running runner connects within a few seconds\n", cfg.Hub, *name)
	fmt.Fprintf(out, "paired as %s\n", *name)
	return nil
}

func pairByQR(hub, name string, open bool, out io.Writer) (*uplink.Config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	claim, err := uplink.StartClaim(ctx, nil, hub, name)
	cancel()
	if err != nil {
		return nil, err
	}
	link := claim.URL(hub)
	fmt.Fprintln(out, "Scan with your phone camera, then tap Confirm in HabitTracker:")
	fmt.Fprintln(out)
	qrterminal.GenerateHalfBlock(link, qrterminal.L, out)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "or open on the phone: %s\n", link)
	if open {
		if err := openURL(link); err != nil {
			fmt.Fprintf(out, "warning: could not open the browser (%v) - use the QR or link above\n", err)
		} else {
			fmt.Fprintln(out, "opened the link in your browser")
		}
	}
	fmt.Fprintf(out, "waiting for confirmation of %q (expires in %d min, Ctrl-C to cancel)...\n", name, int(claim.ExpiresIn.Minutes()))
	ctx, cancel = context.WithTimeout(context.Background(), claim.ExpiresIn)
	defer cancel()
	return uplink.WaitForApproval(ctx, nil, hub, claim, pollEvery)
}

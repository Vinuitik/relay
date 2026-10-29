package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"relay/runner/internal/uplink"
)

// runPair implements `runnerd pair --hub URL --code CODE [--name NAME]`:
// redeem a browser-issued pairing code at the hub and save hub.json in
// relayHome (next to key.txt). See shared/HUB.md "Pairing".
func runPair(args []string, relayHome string, out io.Writer) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(out)
	hub := fs.String("hub", "", "hub base URL, e.g. https://habits.example.com (https required unless localhost)")
	code := fs.String("code", "", "8-char pairing code from the browser")
	name := fs.String("name", "", "device name shown in the browser (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hub == "" || *code == "" {
		return fmt.Errorf("usage: runnerd pair --hub URL --code CODE [--name NAME]")
	}
	if *name == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "runner"
		}
		*name = h
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := uplink.Pair(ctx, nil, *hub, *code, *name)
	if err != nil {
		return err
	}
	if err := uplink.Save(relayHome, *cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "paired as %s\n", *name)
	return nil
}

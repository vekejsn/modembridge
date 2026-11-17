package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"modembridge/internal/config"
	"modembridge/internal/modem"
	"modembridge/internal/service"
)

func main() {
	if err := run(); err != nil {
		slog.Error("modembridge failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "probe" {
		return probe(os.Args[2:])
	}
	f := flag.NewFlagSet("modembridge", flag.ContinueOnError)
	path := f.String("config", "modembridge.json", "JSON configuration file")
	check := f.Bool("check", false, "validate configuration without opening devices or sockets")
	if err := f.Parse(os.Args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *check {
		fmt.Println("Configuration valid")
		return nil
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return (&service.Service{Config: c, Log: log}).Run(ctx)
}

func probe(args []string) error {
	f := flag.NewFlagSet("probe", flag.ContinueOnError)
	baud := f.Int("baud", 19200, "host-side serial rate")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 1 {
		return fmt.Errorf("usage: modembridge probe [-baud 19200] /dev/ttyACM0")
	}
	m, err := modem.Open(config.Modem{Device: f.Arg(0), Baud: *baud})
	if err != nil {
		return err
	}
	defer m.Close()
	identity := ""
	for _, cmd := range []string{"ATQ0V1", "ATI", "ATI3", "ATI7"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		response, err := m.Command(ctx, cmd, "OK")
		cancel()
		fmt.Printf("%s:\n%s\n", cmd, response)
		identity += " " + response
		if err != nil {
			fmt.Printf("Query failed: %v\n", err)
		}
	}
	if strings.Contains(strings.ToLower(identity), "5637") {
		fmt.Println("USR5637 candidate detected. Suggested base: AT&F1, AT&B1&C1&D2&M0&K0. Test B1 (Bell 212A), B15 (V.21), B16 (Bell 103), and S37=2 (V.23) separately. Mixed-mode answering and V.23 reversal remain unverified.")
	} else {
		fmt.Println("Generic modem: supply documented initialization commands; modulation capabilities are unverified.")
	}
	return nil
}

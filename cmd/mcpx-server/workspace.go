package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"mcpx/internal/config"
	"mcpx/internal/observation"
)

type workspaceObserverOptions struct {
	Workspace string
	History   int
	Format    string
}

func parseWorkspaceObserverArgs(args []string) (workspaceObserverOptions, error) {
	fs := flag.NewFlagSet("workspace", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	history := fs.Int("history", observation.DefaultHistory, "number of recent events to replay")
	format := fs.String("format", "text", "text|json")
	if err := fs.Parse(args); err != nil {
		return workspaceObserverOptions{}, err
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		return workspaceObserverOptions{}, fmt.Errorf("workspace name is required")
	}
	if *history <= 0 {
		return workspaceObserverOptions{}, fmt.Errorf("history must be a positive integer")
	}
	if *history > observation.MaxHistory {
		*history = observation.MaxHistory
	}
	*format = strings.ToLower(strings.TrimSpace(*format))
	if *format != "text" && *format != "json" {
		return workspaceObserverOptions{}, fmt.Errorf("format must be text or json")
	}
	return workspaceObserverOptions{Workspace: strings.TrimSpace(fs.Arg(0)), History: *history, Format: *format}, nil
}

func runWorkspaceObserver(args []string) int {
	options, err := parseWorkspaceObserverArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "workspace: %v\n", err)
		printWorkspaceUsage(os.Stderr)
		return 2
	}
	home, err := config.HomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "workspace: resolve MCPX home: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := observation.NewClient(observation.SocketPath(home))
	color := options.Format == "text" && stdoutIsTTY() && os.Getenv("NO_COLOR") == ""
	err = client.Run(ctx, observation.SubscribeRequest{
		Type: "subscribe", Workspace: options.Workspace, HistoryLimit: options.History, Format: options.Format,
	}, func(frame observation.Frame) error {
		return renderWorkspaceFrame(os.Stdout, frame, options.Format, color)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "workspace observer: %v\n", err)
		return 1
	}
	return 0
}

func renderWorkspaceFrame(w io.Writer, frame observation.Frame, format string, color bool) error {
	if frame.Type == "event" && frame.Event != nil {
		if format == "json" {
			return observation.RenderJSON(w, *frame.Event)
		}
		return observation.RenderText(w, *frame.Event, color)
	}
	if format == "json" {
		if frame.Type == "gap" || frame.Type == "error" {
			return json.NewEncoder(w).Encode(frame)
		}
		return nil
	}
	switch frame.Type {
	case "hello":
		_, err := fmt.Fprintf(w, "OBSERVER CONNECTED workspace=%s observer=%s\n", frame.Workspace, frame.ObserverID)
		return err
	case "gap":
		if frame.Gap == nil {
			_, err := fmt.Fprintln(w, "OBSERVER GAP reconnecting")
			return err
		}
		_, err := fmt.Fprintf(w, "OBSERVER GAP sequences=%d-%d; reconnecting\n", frame.Gap.FromSequence, frame.Gap.ToSequence)
		return err
	case "heartbeat":
		return nil
	case "error":
		_, err := fmt.Fprintf(w, "OBSERVER ERROR %s: %s\n", frame.Code, frame.Message)
		return err
	default:
		_, err := fmt.Fprintf(w, "OBSERVER %s\n", frame.Type)
		return err
	}
}

func printWorkspaceUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  mcpx-server workspace [flags] <workspace name>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -history int     recent events to replay (1-200, default 100)")
	fmt.Fprintln(w, "  -format string   text or json (default text)")
}

func stdoutIsTTY() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

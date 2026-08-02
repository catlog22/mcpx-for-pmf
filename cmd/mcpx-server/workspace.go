package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
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
	colorMode := observation.ColorModeNone
	if options.Format == "text" {
		colorMode = terminalColorMode(stdoutIsTTY(), os.Getenv("NO_COLOR"), os.Getenv("COLORTERM"))
	}
	color := colorMode != observation.ColorModeNone
	var textRenderer *observation.TextRenderer
	if options.Format == "text" {
		textRenderer = observation.NewTextRendererWithMode(colorMode, terminalColumns())
	}
	err = client.Run(ctx, observation.SubscribeRequest{
		Type: "subscribe", Workspace: options.Workspace, HistoryLimit: options.History, Format: options.Format,
	}, func(frame observation.Frame) error {
		return renderWorkspaceFrameWithRenderer(os.Stdout, frame, options.Format, color, textRenderer)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "workspace observer: %v\n", err)
		return 1
	}
	return 0
}

func terminalColorMode(isTTY bool, noColor, colorTerm string) observation.ColorMode {
	if !isTTY || strings.TrimSpace(noColor) != "" {
		return observation.ColorModeNone
	}
	switch strings.ToLower(strings.TrimSpace(colorTerm)) {
	case "truecolor", "24bit":
		return observation.ColorModeTrueColor
	default:
		return observation.ColorModeANSI16
	}
}

func renderWorkspaceFrame(w io.Writer, frame observation.Frame, format string, color bool) error {
	var renderer *observation.TextRenderer
	if format == "text" {
		renderer = observation.NewTextRendererWithWidth(color, terminalColumns())
	}
	return renderWorkspaceFrameWithRenderer(w, frame, format, color, renderer)
}

func renderWorkspaceFrameWithRenderer(w io.Writer, frame observation.Frame, format string, color bool, renderer *observation.TextRenderer) error {
	if format == "text" && renderer != nil {
		// Refresh on every frame so a terminal resize cannot leave body lines
		// wider than the current viewport and wrap their continuation at column 0.
		renderer.SetWidth(terminalColumns())
	}
	if frame.Type == "event" && frame.Event != nil {
		if format == "json" {
			return observation.RenderJSON(w, *frame.Event)
		}
		if renderer == nil {
			renderer = observation.NewTextRendererWithWidth(color, terminalColumns())
		}
		return renderer.RenderEvent(w, *frame.Event)
	}
	if format == "json" {
		if frame.Type == "gap" || frame.Type == "error" {
			return json.NewEncoder(w).Encode(frame)
		}
		return nil
	}
	switch frame.Type {
	case "hello":
		return nil
	case "gap":
		if frame.Gap == nil {
			if _, err := fmt.Fprintln(w, "• Reconnected"); err != nil {
				return err
			}
			if renderer != nil {
				renderer.ResetAfterGap()
			}
			return nil
		}
		if _, err := fmt.Fprintln(w, "• Reconnected"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  ↳ recovered events %d-%d\n", frame.Gap.FromSequence, frame.Gap.ToSequence); err != nil {
			return err
		}
		if renderer != nil {
			renderer.ResetAfterGap()
		}
		return nil
	case "heartbeat":
		return nil
	case "error":
		if _, err := fmt.Fprintf(w, "• Failed to observe %s\n", frame.Code); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "  ↳ %s\n", frame.Message)
		return err
	default:
		_, err := fmt.Fprintf(w, "• Observed %s\n", frame.Type)
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

func terminalColumns() int {
	if columns, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && columns > 0 {
		return columns
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return 0
	}
	command := exec.Command("stty", "size")
	command.Stdin = os.Stdin
	output, err := command.Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return 0
	}
	columns, err := strconv.Atoi(fields[1])
	if err != nil || columns <= 0 {
		return 0
	}
	return columns
}

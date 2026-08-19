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
	"path/filepath"
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
	Detail    bool
	Diff      string
	Tool      string
	Status    string
	Operation string
	Path      string
}

type workspaceRegisterOptions struct {
	Path string
}

func parseWorkspaceObserverArgs(args []string) (workspaceObserverOptions, error) {
	fs := flag.NewFlagSet("observe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	history := fs.Int("history", observation.DefaultHistory, "number of recent events to replay")
	format := fs.String("format", "text", "text|json")
	detail := fs.Bool("detail", false, "show semantic purpose, operation IDs, and execution facts")
	diff := fs.String("diff", "full", "summary|preview|full")
	tool := fs.String("tool", "", "filter by tool name")
	status := fs.String("status", "", "filter by event status")
	operation := fs.String("operation", "", "filter by operation ID")
	path := fs.String("path", "", "filter by file path")
	if err := fs.Parse(args); err != nil {
		return workspaceObserverOptions{}, err
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		return workspaceObserverOptions{}, fmt.Errorf("workspace name is required")
	}
	if *history <= 0 {
		return workspaceObserverOptions{}, fmt.Errorf("history must be a positive integer")
	}
	if *history > observation.MaxObserverHistory {
		*history = observation.MaxObserverHistory
	}
	*format = strings.ToLower(strings.TrimSpace(*format))
	if *format != "text" && *format != "json" {
		return workspaceObserverOptions{}, fmt.Errorf("format must be text or json")
	}
	if _, err := observation.ParseDiffMode(*diff); err != nil {
		return workspaceObserverOptions{}, err
	}
	return workspaceObserverOptions{
		Workspace: strings.TrimSpace(fs.Arg(0)), History: *history, Format: *format, Detail: *detail,
		Diff: strings.ToLower(strings.TrimSpace(*diff)), Tool: strings.TrimSpace(*tool),
		Status: strings.TrimSpace(*status), Operation: strings.TrimSpace(*operation), Path: strings.TrimSpace(*path),
	}, nil
}

func parseWorkspaceRegisterArgs(args []string) (workspaceRegisterOptions, error) {
	fs := flag.NewFlagSet("workspace register", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return workspaceRegisterOptions{}, err
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		return workspaceRegisterOptions{}, fmt.Errorf("workspace path is required")
	}
	return workspaceRegisterOptions{Path: strings.TrimSpace(fs.Arg(0))}, nil
}

func runWorkspaceCommand(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printWorkspaceCommandUsage(os.Stderr)
		return 0
	}
	if args[0] != "register" && args[0] != "remove" {
		fmt.Fprintf(os.Stderr, "workspace: unknown command %q\n", args[0])
		printWorkspaceCommandUsage(os.Stderr)
		return 2
	}
	if args[0] == "remove" {
		return runWorkspaceRemove(args[1:])
	}
	return runWorkspaceRegister(args[1:])
}

func runWorkspaceRegister(args []string) int {
	options, err := parseWorkspaceRegisterArgs(args)
	if err != nil {
		if err == flag.ErrHelp {
			printWorkspaceRegisterUsage(os.Stderr)
			return 0
		}
		fmt.Fprintf(os.Stderr, "workspace register: %v\n", err)
		printWorkspaceRegisterUsage(os.Stderr)
		return 2
	}
	path := config.ExpandHome(options.Path)
	absPath, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "workspace register: resolve path: %v\n", err)
		return 1
	}
	if err := config.RegisterWorkspace("", absPath); err != nil {
		fmt.Fprintf(os.Stderr, "workspace register: %v\n", err)
		return 1
	}
	fmt.Printf("已注册 Workspace：%s\n路径：%s\n", filepath.Base(absPath), absPath)
	return 0
}

func runWorkspaceRemove(args []string) int {
	options, err := parseWorkspaceRegisterArgs(args)
	if err != nil {
		if err == flag.ErrHelp {
			printWorkspaceRegisterUsage(os.Stderr)
			return 0
		}
		fmt.Fprintf(os.Stderr, "workspace remove: %v\n", err)
		printWorkspaceRegisterUsage(os.Stderr)
		return 2
	}
	path := config.ExpandHome(options.Path)
	absPath, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "workspace remove: resolve path: %v\n", err)
		return 1
	}
	if err := config.UnregisterWorkspace("", absPath); err != nil {
		fmt.Fprintf(os.Stderr, "workspace remove: %v\n", err)
		return 1
	}
	fmt.Printf("已移除 Workspace：%s\n路径：%s\n", filepath.Base(absPath), absPath)
	return 0
}

func runObserve(args []string) int {
	return runObserver(args, "observe")
}

func runObserver(args []string, commandName string) int {
	options, err := parseWorkspaceObserverArgs(args)
	if err != nil {
		if err == flag.ErrHelp {
			printObserveUsage(os.Stderr)
			return 0
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", commandName, err)
		printObserveUsage(os.Stderr)
		return 2
	}
	home, err := config.HomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: resolve MCPX home: %v\n", commandName, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := observation.NewClient(observation.SocketPath(home))
	isTTY := stdoutIsTTY()
	colorMode := observation.ColorModeNone
	if options.Format == "text" {
		colorMode = terminalColorMode(isTTY, os.Getenv("NO_COLOR"), os.Getenv("COLORTERM"))
	}
	color := colorMode != observation.ColorModeNone
	var textRenderer *observation.TextRenderer
	if options.Format == "text" {
		textRenderer = observation.NewTextRendererWithMode(colorMode, terminalColumns())
		textRenderer.SetDetail(options.Detail)
		diffMode, _ := observation.ParseDiffMode(options.Diff)
		textRenderer.SetDiffMode(diffMode)
		textRenderer.SetFilter(observation.EventFilter{
			Tool: options.Tool, Status: options.Status, OperationID: options.Operation, Path: options.Path,
		})
	}

	request := observation.SubscribeRequest{
		Type: "subscribe", Workspace: options.Workspace, HistoryLimit: options.History, Format: options.Format,
	}
	err = client.Run(ctx, request, func(frame observation.Frame) error {
		return renderWorkspaceFrameWithRenderer(os.Stdout, frame, options.Format, color, textRenderer)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", commandName, err)
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
	return renderWorkspaceFrameWithRendererAtWidth(w, frame, format, color, renderer, terminalColumns())
}

func renderWorkspaceFrameWithRendererAtWidth(w io.Writer, frame observation.Frame, format string, color bool, renderer *observation.TextRenderer, terminalWidth int) error {
	if format == "text" && renderer != nil {
		// Refresh on every frame so a terminal resize cannot leave newly-rendered
		// lines wider than the current viewport.
		renderer.SetWidth(terminalWidth)
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
		if _, err := fmt.Fprintf(w, "  recovered events %d-%d\n", frame.Gap.FromSequence, frame.Gap.ToSequence); err != nil {
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
		_, err := fmt.Fprintf(w, "  %s\n", frame.Message)
		return err
	default:
		_, err := fmt.Fprintf(w, "• Observed %s\n", frame.Type)
		return err
	}
}

func printObserveUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  mcpx observe [flags] <workspace name>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Observe persisted Workspace events and terminal output.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -history int     recent events to replay (1-100, default 100)")
	fmt.Fprintln(w, "  -format string   text or json (default text)")
	fmt.Fprintln(w, "  -detail          show semantic purpose, operation IDs, and execution facts")
	fmt.Fprintln(w, "  -diff string     summary, preview, or full (default full)")
	fmt.Fprintln(w, "  -tool string     filter by tool name")
	fmt.Fprintln(w, "  -status string   filter by event status")
	fmt.Fprintln(w, "  -operation string filter by operation ID")
	fmt.Fprintln(w, "  -path string     filter by file path")
}

func printWorkspaceCommandUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  mcpx workspace register <path>")
	fmt.Fprintln(w, "  mcpx workspace remove <path>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Register or update a Workspace in the global config without starting the Runtime;")
	fmt.Fprintln(w, "remove deletes the matching entry.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "For terminal observation, use:")
	fmt.Fprintln(w, "  mcpx observe [flags] <workspace name>")
}

func printWorkspaceRegisterUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  mcpx workspace register <path>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Register or update a Workspace in the global config without starting the Runtime.")
}

func stdoutIsTTY() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func terminalColumns() int {
	columns, _ := terminalSize()
	return columns
}

func terminalSize() (columns, rows int) {
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && value > 0 {
		columns = value
	}
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("LINES"))); err == nil && value > 0 {
		rows = value
	}
	if columns > 0 && rows > 0 {
		return columns, rows
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return columns, rows
	}
	command := exec.Command("stty", "size")
	command.Stdin = os.Stdin
	output, err := command.Output()
	if err != nil {
		return columns, rows
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return columns, rows
	}
	if rows <= 0 {
		if value, err := strconv.Atoi(fields[0]); err == nil && value > 0 {
			rows = value
		}
	}
	if columns <= 0 {
		if value, err := strconv.Atoi(fields[1]); err == nil && value > 0 {
			columns = value
		}
	}
	return columns, rows
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/changeset"
	"mcpx/internal/envelope"
	"mcpx/internal/observation"
	"mcpx/internal/remotesession"
)

func TestObservationRecordsToolLifecycleAndRedacts(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	request := mcpresult.Request(map[string]any{
		"intent":    "inspect the project configuration",
		"workspace": "demo",
		"token":     "do-not-store-this-token",
		"path":      "config.yaml",
	})

	wrapper := rt.instrumentTool("observer_test", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText(`{"message":"visible output","password":"do-not-store-this-password"}`), nil
	})
	if _, err := wrapper(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	var started, completed *observationEventView
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := rt.observation.store.History(context.Background(), "demo", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		started, completed = nil, nil
		for _, event := range events {
			if event.Tool != "observer_test" {
				continue
			}
			view := observationEventView{Type: event.Type, Sequence: event.Sequence, CallID: event.CallID, Phase: event.Phase, Intent: event.Intent, Input: string(event.Input), Output: string(event.Output)}
			switch event.Type {
			case "tool.started":
				started = &view
			case "tool.completed":
				completed = &view
			}
		}
		if started != nil && completed != nil && started.Sequence < completed.Sequence {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if started == nil || completed == nil || started.Sequence >= completed.Sequence {
		t.Fatalf("tool lifecycle order invalid: started=%+v completed=%+v", started, completed)
	}
	if started.Intent != "inspect the project configuration" {
		t.Fatalf("intent=%q", started.Intent)
	}
	if started.CallID == "" || started.CallID != completed.CallID || started.Phase != observation.PhaseActionStarted || completed.Phase != observation.PhaseResult {
		t.Fatalf("event correlation/phase invalid: started=%+v completed=%+v", started, completed)
	}
	if strings.Contains(started.Input, "do-not-store-this-token") || strings.Contains(completed.Output, "do-not-store-this-password") {
		t.Fatalf("sensitive tool data leaked: input=%s output=%s", started.Input, completed.Output)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(completed.Output), &output); err != nil {
		t.Fatal(err)
	}
	if output["status"] != "succeeded" {
		t.Fatalf("completed status=%+v", output)
	}

	errorRequest := mcpresult.Request(map[string]any{"intent": "run the failing observer operation", "workspace": "demo"})

	errorWrapper := rt.instrumentTool("observer_error_test", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("observer operation failed")
	})
	if _, err := errorWrapper(context.Background(), errorRequest); err == nil {
		t.Fatal("expected handler error")
	}
	var errorOutput string
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := rt.observation.store.History(context.Background(), "demo", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		errorOutput = ""
		for _, event := range events {
			if event.Tool == "observer_error_test" && event.Type == "tool.completed" {
				errorOutput = string(event.Output)
			}
		}
		if errorOutput != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var errorView map[string]any
	if err := json.Unmarshal([]byte(errorOutput), &errorView); err != nil {
		t.Fatal(err)
	}
	if errorView["status"] != "failed" || !strings.Contains(errorOutput, "observer operation failed") {
		t.Fatalf("error completion was not observable: %s", errorOutput)
	}
}

func TestObservationAggregatesRemoteSessionLifecycleByWorkspace(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	for i := 0; i < 2; i++ {
		created, createErr := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
			WorkspaceName: "demo", WorkspacePath: registered.Path,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if created.Session.ID == "" {
			t.Fatal("remote session id missing")
		}
	}
	events, err := rt.observation.store.History(context.Background(), "demo", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		if event.Type == "session.lifecycle" && strings.Contains(string(event.Output), `"source_type":"remote_session.created"`) {
			seen[event.RemoteSessionID] = true
		}
	}
	if len(seen) != 2 {
		t.Fatalf("workspace history did not aggregate both sessions: %+v", events)
	}
}

func TestObservationRecordsAppliedChangesetWithFileDiff(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := rt.changesets.Prepare(context.Background(), created.Session.ID, principal.ID, registered.Path, "create observed file", []changeset.Operation{{
		Operation: "create", Path: "observed.txt", Content: "visible change\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	envReq := envelope.Request{
		RequestID:       "req_observed_change",
		Intent:          "apply the observed file change",
		RemoteSessionID: created.Session.ID,
		Workspace:       "demo",
		Payload:         map[string]any{},
	}
	if _, err := rt.applyChangeset(context.Background(), envReq, principal.ID, created.Session, item); err != nil {
		t.Fatal(err)
	}
	events, err := rt.observation.store.History(context.Background(), "demo", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var changed *observationEventView
	for _, event := range events {
		if event.Type == "file.changed" && event.OperationID == item.ID {
			view := observationEventView{Intent: event.Intent, Output: string(event.Output)}
			changed = &view
		}
	}
	if changed == nil || changed.Intent != envReq.Intent {
		t.Fatalf("file change event missing: %+v", changed)
	}
	if !strings.Contains(changed.Output, "observed.txt") || !strings.Contains(changed.Output, "visible change") {
		t.Fatalf("file change event lacks concrete content: %s", changed.Output)
	}
}

func TestObservationRecordsRuntimeTaskOutput(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	const requestID = "req_runtime_output"
	const tool = "command_execute"
	command := "printf 'runtime-out token=do-not-store-this-token'; printf 'runtime-err password=do-not-store-this-password' >&2"
	task, err := rt.tasks.StartRemoteWithObservation(context.Background(), requestID, tool, created.Session.ID, "demo", registered.Path, command)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !task.Wait(waitCtx) {
		t.Fatal("runtime task did not exit")
	}
	events, err := rt.observation.store.History(context.Background(), "demo", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	expectedResourceURI := fmt.Sprintf("mcpx://remote-sessions/%s/tasks/%s/logs", created.Session.ID, task.ID)
	streams := map[string]string{}
	offsets := map[string]int64{}
	for _, event := range events {
		if event.Type != "command.output" || event.OperationID != task.ID {
			continue
		}
		if event.Workspace != "demo" || event.RemoteSessionID != created.Session.ID || event.RequestID != requestID || event.Tool != tool {
			t.Fatalf("runtime output identity=%+v", event)
		}
		if event.Stream != "stdout" && event.Stream != "stderr" {
			t.Fatalf("runtime output stream=%q", event.Stream)
		}
		if event.Offset != offsets[event.Stream] {
			t.Fatalf("runtime output offset=%d for %s, want %d", event.Offset, event.Stream, offsets[event.Stream])
		}
		if event.ResourceURI != expectedResourceURI {
			t.Fatalf("runtime output resource=%q, want %q", event.ResourceURI, expectedResourceURI)
		}
		var output map[string]any
		if err := json.Unmarshal(event.Output, &output); err != nil {
			t.Fatal(err)
		}
		text, ok := output["text"].(string)
		if !ok {
			t.Fatalf("runtime output text=%+v", output["text"])
		}
		if strings.Contains(string(event.Output), command) || strings.Contains(string(event.Output), "do-not-store-this-token") || strings.Contains(string(event.Output), "do-not-store-this-password") {
			t.Fatalf("runtime output leaked command or credential: %s", event.Output)
		}
		streams[event.Stream] += text
		bytesValue, ok := output["bytes"].(float64)
		if !ok {
			t.Fatalf("runtime output bytes=%+v", output["bytes"])
		}
		offsets[event.Stream] += int64(bytesValue)
	}
	if streams["stdout"] != "runtime-out token=[REDACTED]" || streams["stderr"] != "runtime-err password=[REDACTED]" {
		t.Fatalf("runtime output events=%+v", streams)
	}
}

func TestObservationRecordsCommandTaskRequestIdentity(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	envReq := envelope.Request{
		RequestID:       "req_command_observed",
		Intent:          "observe command output",
		RemoteSessionID: created.Session.ID,
		Workspace:       "demo",
		Payload:         map[string]any{},
	}
	if _, err := rt.executeCommandTask(context.Background(), envReq, principal, created.Session, "printf 'command-out'", time.Second, "test", "workspace", "sha256:test"); err != nil {
		t.Fatal(err)
	}

	events, err := rt.observation.store.History(context.Background(), "demo", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type != observation.TypeCommandOutput {
			continue
		}
		var output map[string]any
		if err := json.Unmarshal(event.Output, &output); err != nil {
			t.Fatal(err)
		}
		if output["text"] != "command-out" {
			continue
		}
		found = true
		if event.RequestID != envReq.RequestID || event.Tool != "command_execute" {
			t.Fatalf("command output identity=%+v, want request=%q tool=%q", event, envReq.RequestID, "command_execute")
		}
	}
	if !found {
		t.Fatal("command output event missing")
	}
}

func TestObservationRecordsChangeVerificationRequestIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("make-based verification fixture is not portable to windows")
	}
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	if err := os.WriteFile(filepath.Join(registered.Path, "Makefile"), []byte("verify:\n\tprintf 'verify-out'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	envReq := envelope.Request{
		RequestID:       "req_change_verify",
		Intent:          "verify observed change",
		RemoteSessionID: created.Session.ID,
		Workspace:       "demo",
		Payload:         map[string]any{},
	}
	results := rt.runVerifySteps(context.Background(), envReq, created.Session, []string{"verify"})
	if len(results) != 1 || fmt.Sprint(results[0]["status"]) != "exited" {
		t.Fatalf("verification did not run: %+v", results)
	}

	events, err := rt.observation.store.History(context.Background(), "demo", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type != observation.TypeCommandOutput {
			continue
		}
		var output map[string]any
		if err := json.Unmarshal(event.Output, &output); err != nil {
			t.Fatal(err)
		}
		if output["text"] != "verify-out" {
			continue
		}
		found = true
		if event.RequestID != envReq.RequestID || event.Tool != "change_execute" {
			t.Fatalf("verification output identity=%+v, want request=%q tool=%q", event, envReq.RequestID, "change_execute")
		}
	}
	if !found {
		t.Fatal("verification output event missing")
	}
}

func TestObservationSocketEndToEndDeliversToolEvents(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	socketPath := testObservationSocketPath(t)
	rt.observerSocket = observation.NewSocketServer(socketPath, rt.observation.store, rt.observation.broker, func(name string) bool {
		_, ok := rt.reg.Get(name)
		return ok
	})
	if err := rt.observerSocket.Start(); err != nil {
		t.Fatal(err)
	}
	defer rt.observerSocket.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	frames := make(chan observation.Frame, 8)
	client := observation.NewClient(socketPath)
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, observation.SubscribeRequest{Workspace: "demo", HistoryLimit: 20}, func(frame observation.Frame) error {
			frames <- frame
			if frame.Type == "event" && frame.Event != nil && frame.Event.Tool == "observer_e2e" && frame.Event.Type == observation.TypeToolCompleted {
				cancel()
			}
			return nil
		})
	}()
	select {
	case frame := <-frames:
		if frame.Type != "hello" {
			t.Fatalf("first observer frame=%+v", frame)
		}
	case <-ctx.Done():
		t.Fatal("observer did not connect")
	}
	wrapper := rt.instrumentTool("observer_e2e", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText("e2e output"), nil
	})
	request := mcpresult.Request(map[string]any{"intent": "verify observer delivery", "workspace": "demo"})

	if _, err := wrapper(ctx, request); err != nil {
		t.Fatal(err)
	}
	events := map[string]bool{}
	eventCount := 0
	deadline := time.After(2 * time.Second)
	for len(events) < 2 {
		select {
		case frame := <-frames:
			if frame.Type == "event" && frame.Event != nil && frame.Event.Tool == "observer_e2e" {
				eventCount++
				if frame.Event.Sequence <= 0 {
					t.Fatalf("live event was not durable: %+v", frame.Event)
				}
				events[frame.Event.Type] = true
			}
		case <-deadline:
			t.Fatalf("observer events=%+v", events)
		}
	}
	if !events[observation.TypeToolStarted] || !events[observation.TypeToolCompleted] {
		t.Fatalf("missing tool lifecycle events=%+v", events)
	}
	if eventCount != 2 {
		t.Fatalf("durable-first bridge duplicated live events: count=%d", eventCount)
	}
	cancel()
	select {
	case err := <-clientErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer client did not stop after cancellation")
	}
}

type observationEventView struct {
	Type     string
	Sequence int64
	CallID   string
	Phase    string
	Intent   string
	Input    string
	Output   string
}

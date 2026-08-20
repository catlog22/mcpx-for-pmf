package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePiOwnerSnapshot(t *testing.T, root, ownerID, ownerNonce, workspaceID string, publishedAt int64) string {
	t.Helper()
	dir := filepath.Join(root, "owners")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]any{
		"version": 1, "kind": "owner", "workspaceId": workspaceID, "normalizedCwd": "d:/fake",
		"ownerId": ownerID, "ownerNonce": ownerNonce, "pid": 1234, "publishedAt": publishedAt,
		"sessionName": "demo-window", "agents": []any{}, "settled": []any{},
	}
	raw, _ := json.Marshal(snapshot)
	path := filepath.Join(dir, ownerID+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func withFakePeerRoot(t *testing.T, fn func(root string)) {
	t.Helper()
	root := t.TempDir()
	previous := resolvePiPeerRuntimeRoot
	resolvePiPeerRuntimeRoot = func(string) (string, error) { return root, nil }
	t.Cleanup(func() { resolvePiPeerRuntimeRoot = previous })
	fn(root)
}

func TestPiPeerWorkspaceIDMatchesPlugin(t *testing.T) {
	// Vector produced by the pi plugin's workspaceIdForCwd for D:/pi-maestro-flow.
	previous := resolvePiPeerRuntimeRoot
	defer func() { resolvePiPeerRuntimeRoot = previous }()
	t.Setenv("USERPROFILE", filepath.Join(t.TempDir(), "home"))
	root, err := defaultPiPeerRuntimeRoot("D:/pi-maestro-flow")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(root, "7b43995641bf8459224295d6ff3bfe4608ce4da280e0968b42bf9a2a0e320269") {
		t.Fatalf("workspaceId mismatch: %s", root)
	}
}

func TestListPiWindowsFiltersFreshAndValid(t *testing.T) {
	withFakePeerRoot(t, func(root string) {
		workspaceID := strings.Repeat("a", 64)
		now := time.Now().UnixMilli()
		writePiOwnerSnapshot(t, root, strings.Repeat("1", 32), strings.Repeat("b", 32), workspaceID, now)
		writePiOwnerSnapshot(t, root, strings.Repeat("2", 32), strings.Repeat("c", 32), workspaceID, now-60_000) // stale
		// invalid JSON file
		if err := os.WriteFile(filepath.Join(root, "owners", "junk.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshots, listings, err := listPiWindows("D:/fake", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshots) != 1 || len(listings) != 1 {
			t.Fatalf("expected 1 fresh window, got snapshots=%d listings=%d", len(snapshots), len(listings))
		}
		if listings[0].DisplayName != "demo-window" || listings[0].OwnerID != strings.Repeat("1", 32) {
			t.Fatalf("unexpected listing: %+v", listings[0])
		}
	})
}

func TestDeliverPiWindowCommandWritesProtocolFile(t *testing.T) {
	withFakePeerRoot(t, func(root string) {
		workspaceID := strings.Repeat("a", 64)
		target := piOwnerSnapshot{
			Version: 1, Kind: "owner", WorkspaceID: workspaceID,
			OwnerID: strings.Repeat("1", 32), OwnerNonce: strings.Repeat("b", 32),
		}
		identity := piPeerIdentity{OwnerID: strings.Repeat("f", 32), OwnerNonce: strings.Repeat("e", 32)}
		command, response, err := deliverPiWindowCommand("D:/fake", target, identity, strings.Repeat("7", 32), "steer", "do the thing", 100*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if response != nil {
			t.Fatalf("expected no response yet, got %+v", response)
		}
		if !strings.HasSuffix(command.CommandID, ".json") && command.CommandID == "" {
			t.Fatal("command id missing")
		}
		raw, err := os.ReadFile(filepath.Join(root, "commands", target.OwnerID, command.CommandID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var written piPeerCommand
		if err := json.Unmarshal(raw, &written); err != nil {
			t.Fatal(err)
		}
		if written.Version != 1 || written.Kind != "command" || written.WorkspaceID != workspaceID ||
			written.FromOwnerID != identity.OwnerID || written.FromOwnerNonce != identity.OwnerNonce ||
			written.ToOwnerID != target.OwnerID || written.ToOwnerNonce != target.OwnerNonce ||
			written.TargetCorrelation != piPeerWindowMainSession || written.Action != "steer" ||
			written.Message != "do the thing" || written.MessageKind != "request" || written.Source != "system" ||
			written.ReplyTo != "owner:"+identity.OwnerID {
			t.Fatalf("protocol mismatch: %+v", written)
		}
		if written.ExpiresAt <= written.CreatedAt || written.ExpiresAt-written.CreatedAt > int64(piPeerCommandTTL/time.Millisecond) {
			t.Fatalf("ttl out of bounds: created=%d expires=%d", written.CreatedAt, written.ExpiresAt)
		}
	})
}

func TestDeliverPiWindowCommandReadsResponse(t *testing.T) {
	withFakePeerRoot(t, func(root string) {
		workspaceID := strings.Repeat("a", 64)
		target := piOwnerSnapshot{
			Version: 1, Kind: "owner", WorkspaceID: workspaceID,
			OwnerID: strings.Repeat("1", 32), OwnerNonce: strings.Repeat("b", 32),
		}
		identity := piPeerIdentity{OwnerID: strings.Repeat("f", 32), OwnerNonce: strings.Repeat("e", 32)}
		command, _, err := deliverPiWindowCommand("D:/fake", target, identity, strings.Repeat("8", 32), "follow_up", "hi", 100*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		response := piPeerCommandResponse{
			Version: 1, Kind: "response", WorkspaceID: workspaceID, CommandID: command.CommandID,
			FromOwnerID: target.OwnerID, FromOwnerNonce: target.OwnerNonce,
			ToOwnerID: identity.OwnerID, ToOwnerNonce: identity.OwnerNonce,
			TargetCorrelation: piPeerWindowMainSession, Status: "accepted",
			EffectiveAction: "follow_up", DeliveryStage: "queued",
			RespondedAt: time.Now().UnixMilli(), ExpiresAt: time.Now().UnixMilli() + 86_400_000,
		}
		raw, _ := json.Marshal(response)
		responseDir := filepath.Join(root, "responses", identity.OwnerID)
		if err := os.MkdirAll(responseDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(responseDir, command.CommandID+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		received, err := waitForPiPeerResponse(filepath.Join(responseDir, command.CommandID+".json"), command.CommandID, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if received == nil || received.Status != "accepted" || received.DeliveryStage != "queued" {
			t.Fatalf("expected accepted response, got %+v", received)
		}
	})
}

func TestPiWindowToolListAndConfirmationGate(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	_, remoteID := openTestRemote(t, rt)
	withFakePeerRoot(t, func(root string) {
		workspaceID := strings.Repeat("a", 64)
		writePiOwnerSnapshot(t, root, strings.Repeat("1", 32), strings.Repeat("b", 32), workspaceID, time.Now().UnixMilli())

		// list
		listed := callEnvelope(t, rt.toolPiWindow, context.Background(), map[string]any{
			"action": "list", "remote_session_id": remoteID,
		})
		data, _ := listed["data"].(map[string]any)
		windows, _ := data["windows"].([]any)
		if listed["status"] != "ok" || len(windows) != 1 {
			t.Fatalf("list failed: %+v", listed)
		}

		// send without confirmation -> confirmation required (default mode steer)
		response := callEnvelope(t, rt.toolPiWindow, context.Background(), map[string]any{
			"action": "send", "remote_session_id": remoteID,
			"window": strings.Repeat("1", 32), "message": "please do X",
		})
		if errorCode(response) != "user_confirmation_required" {
			t.Fatalf("expected user_confirmation_required, got %+v", response)
		}
		if data, _ := response["data"].(map[string]any); data == nil || data["action"] != "steer" {
			t.Fatalf("expected default steer mode in confirmation data, got %+v", response)
		}

		// send to unknown window
		response = callEnvelope(t, rt.toolPiWindow, context.Background(), map[string]any{
			"action": "send", "remote_session_id": remoteID,
			"window": strings.Repeat("9", 32), "message": "please do X", "user_confirmed": true,
		})
		if errorCode(response) != "window_not_found" {
			t.Fatalf("expected window_not_found, got %+v", response)
		}

		// message with control characters
		response = callEnvelope(t, rt.toolPiWindow, context.Background(), map[string]any{
			"action": "send", "remote_session_id": remoteID,
			"window": strings.Repeat("1", 32), "message": "line1\nline2",
		})
		if errorCode(response) != "bad_request" {
			t.Fatalf("expected bad_request for control chars, got %+v", response)
		}
	})
}

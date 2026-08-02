package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestStreamableHTTPReconnectRestoresRemoteSession(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "project")
	protocol := mcpserver.NewMCPServer("mcpx", runtime.build.Version, mcpserver.WithToolCapabilities(true))
	runtime.registerTools(protocol)
	streamable := mcpserver.NewStreamableHTTPServer(protocol, mcpserver.WithDisableLocalhostProtection(true))
	httpServer := httptest.NewServer(NewGateway(runtime.cfg, nil, streamable).Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	first := newReconnectClient(t, httpServer.URL+"/mcp")
	if err := first.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Initialize(ctx, reconnectInitializeRequest()); err != nil {
		t.Fatal(err)
	}
	firstTools, err := first.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	firstNames := toolNames(firstTools.Tools)
	firstData := callReconnectTool(t, ctx, first, "session_open", map[string]any{
		"workspace":         "project",
		"client_request_id": "chatgpt-reconnect-test",
	})
	remoteSession, _ := firstData["remote_session"].(map[string]any)
	remoteID, _ := remoteSession["id"].(string)
	if remoteID == "" {
		t.Fatalf("missing remote session: %+v", firstData)
	}
	revisions, _ := firstData["revisions"].(map[string]any)
	toolRevision := revisions["tool_schema_revision"]
	if toolRevision == nil || toolRevision == "" {
		t.Fatalf("missing tool revision: %+v", firstData)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := newReconnectClient(t, httpServer.URL+"/mcp")
	defer second.Close()
	if err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Initialize(ctx, reconnectInitializeRequest()); err != nil {
		t.Fatal(err)
	}
	secondTools, err := second.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(secondTools.Tools); len(got) != len(firstNames) || !sameStrings(got, firstNames) {
		t.Fatalf("tools changed across transport reconnect: first=%v second=%v", firstNames, got)
	}
	resumed := callReconnectTool(t, ctx, second, "session_open", map[string]any{
		"remote_session_id": remoteID,
		"known_revisions": map[string]any{
			"tool_schema_revision":        revisions["tool_schema_revision"],
			"skill_revision":              revisions["skill_revision"],
			"mcp_revision":                revisions["mcp_revision"],
			"instruction_revision":        revisions["instruction_revision"],
			"session_capability_revision": revisions["session_capability_revision"],
		},
	})
	resumedSession, _ := resumed["remote_session"].(map[string]any)
	if resumedSession["id"] != remoteID {
		t.Fatalf("remote session changed: first=%s resumed=%+v", remoteID, resumedSession)
	}
	resumedRefresh, _ := resumed["client_refresh"].(map[string]any)
	if resumedRefresh["required"] != false {
		t.Fatalf("unexpected refresh request: %+v", resumedRefresh)
	}
	if resumed["workspace"].(map[string]any)["name"] != "project" {
		t.Fatalf("workspace was not restored: %+v", resumed["workspace"])
	}

	refresh := clientRefreshPayload(map[string]any{"known_revisions": map[string]any{"tool_schema_revision": "old"}}, map[string]any{"tool_schema_revision": toolRevision})
	if refresh["required"] != true {
		t.Fatalf("schema change did not require refresh: %+v", refresh)
	}
	actions, _ := refresh["actions"].([]string)
	if len(actions) != 3 || actions[0] != "reconnect" || actions[1] != "tools/list" || actions[2] != "session_open" {
		t.Fatalf("refresh actions = %v", actions)
	}
}

func newReconnectClient(t *testing.T, endpoint string) *mcpclient.Client {
	t.Helper()
	client, err := mcpclient.NewStreamableHttpClient(endpoint, mcptransport.WithHTTPHeaders(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func reconnectInitializeRequest() mcp.InitializeRequest {
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: "chatgpt-reconnect-test", Version: "1.0.0"}
	return request
}

func callReconnectTool(t *testing.T, ctx context.Context, client *mcpclient.Client, name string, arguments map[string]any) map[string]any {
	t.Helper()
	if _, exists := arguments["intent"]; !exists {
		withIntent := make(map[string]any, len(arguments)+1)
		for key, value := range arguments {
			withIntent[key] = value
		}
		withIntent["intent"] = "reconnect acceptance operation"
		arguments = withIntent
	}
	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = arguments
	result, err := client.CallTool(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) == 0 {
		t.Fatal("empty tool result")
	}
	if result.StructuredContent == nil {
		t.Fatalf("tool structured content missing: %+v", result.Content[0])
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	mcpx, _ := envelope["mcpx"].(map[string]any)
	resultData, _ := mcpx["result"].(map[string]any)
	data, _ := resultData["data"].(map[string]any)
	if data == nil {
		t.Fatalf("tool data missing: %+v", envelope)
	}
	return data
}

func toolNames(tools []mcp.Tool) []string {
	result := make([]string, 0, len(tools))
	for _, tool := range tools {
		result = append(result, tool.Name)
	}
	sort.Strings(result)
	return result
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

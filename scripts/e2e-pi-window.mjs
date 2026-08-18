// mcpx-for-pmf pi_window end-to-end verification:
// session open -> pi_window list (windows auto-registered by running pi)
// -> pi_window send (confirmation gate) -> user_confirmed retry -> accepted
// Usage: node e2e-pi-window.mjs [endpoint] [workspace] [message]
const endpoint = process.argv[2] ?? "http://127.0.0.1:9090/mcp";
const workspace = process.argv[3] ?? "pi-maestro-flow";
const message = process.argv[4] ?? "Reply with exactly: PONG";

let nextId = 0;
let sessionId = null;

async function rpc(method, params) {
  const id = ++nextId;
  const headers = { "Content-Type": "application/json", Accept: "application/json, text/event-stream" };
  if (sessionId) headers["Mcp-Session-Id"] = sessionId;
  const response = await fetch(endpoint, { method: "POST", headers, body: JSON.stringify({ jsonrpc: "2.0", id, method, params }) });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  const incoming = response.headers.get("mcp-session-id");
  if (incoming) sessionId = incoming;
  return parsePayload(response, method);
}

async function notify(method, params) {
  const headers = { "Content-Type": "application/json", Accept: "application/json, text/event-stream" };
  if (sessionId) headers["Mcp-Session-Id"] = sessionId;
  const response = await fetch(endpoint, { method: "POST", headers, body: JSON.stringify({ jsonrpc: "2.0", method, params }) });
  if (!response.ok) throw new Error(`HTTP ${response.status} for ${method}`);
  const incoming = response.headers.get("mcp-session-id");
  if (incoming) sessionId = incoming;
  return null;
}

async function parsePayload(response, method) {
  const contentType = response.headers.get("content-type") ?? "";
  const raw = await response.text();
  if (contentType.includes("text/event-stream")) {
    let payloadText = null;
    for (const line of raw.split(/\r?\n/)) {
      if (line.startsWith("data:")) payloadText = line.slice(5).trim();
    }
    if (payloadText) return JSON.parse(payloadText).result;
    throw new Error(`${method}: empty SSE payload`);
  }
  const payload = JSON.parse(raw);
  if (payload.error) throw new Error(`${method}: ${JSON.stringify(payload.error)}`);
  return payload.result;
}

const textOf = (result) => {
  const parts = [];
  for (const item of result?.content ?? []) if (item?.type === "text") parts.push(item.text);
  if (result?.structuredContent) parts.push(JSON.stringify(result.structuredContent));
  return parts.join("\n");
};

const init = await rpc("initialize", { protocolVersion: "2025-11-25", capabilities: {}, clientInfo: { name: "e2e-pi-window", version: "1.0.0" } });
console.log("initialize ok, server:", init.serverInfo?.name, init.serverInfo?.version);
await notify("notifications/initialized", {});

const opened = await rpc("tools/call", { name: "session", arguments: { action: "open", workspace } });
const remoteId = textOf(opened).match(/remote_session_id"?[: ]+"([^"]+)/)?.[1];
if (!remoteId) { console.error("FAIL: session open", textOf(opened).slice(0, 400)); process.exit(1); }
console.log("PASS: session open:", remoteId);

// 1) list windows
const listed = await rpc("tools/call", { name: "pi_window", arguments: { action: "list", remote_session_id: remoteId } });
const listText = textOf(listed);
const windows = (JSON.parse(listText.split("\n").find((l) => l.includes('"windows"')) || listText)?.data?.windows)
  ?? [...listText.matchAll(/"display_name"?[: ]+"([^"]+)"/g)].map((m) => ({ display_name: m[1] }));
console.log("windows found:", windows.length);
if (windows.length === 0) {
  console.error("FAIL: no fresh pi windows discoverable — start a pi window in this workspace first", listText.slice(0, 800));
  process.exit(1);
}
const first = windows[0];
console.log("target window:", JSON.stringify(first));

// 2) send -> confirmation gate
const sendArgs = { action: "send", remote_session_id: remoteId, window: first.owner_id ?? first.display_name, message, mode: "steer", purpose: "e2e verify pi window dispatch" };
const gated = await rpc("tools/call", { name: "pi_window", arguments: sendArgs });
const gatedText = textOf(gated);
console.log("confirmation gate status:", /waiting_confirmation|user_confirmation_required/i.test(gatedText) ? "CONFIRMATION_REQUIRED (expected)" : "unexpected", gatedText.slice(0, 300));
if (!/user_confirmation_required|waiting_confirmation/i.test(gatedText)) {
  console.error("FAIL: expected confirmation gate", gatedText.slice(0, 800));
  process.exit(1);
}
console.log("PASS: send requires user confirmation");

// 3) confirmed retry -> delivery accepted
const dispatched = await rpc("tools/call", { name: "pi_window", arguments: { ...sendArgs, user_confirmed: true, wait_time_ms: 30000 } });
const dispatchText = textOf(dispatched);
console.log("dispatch result:", dispatchText.slice(0, 800));
if (!/"delivered"?[: ]true|"delivery"?[: ]"accepted"/.test(dispatchText) && !/"delivery"?[: ]"accepted"/.test(dispatchText)) {
  console.error("FAIL: delivery not accepted", dispatchText.slice(0, 1500));
  process.exit(1);
}
console.log("PASS: task delivered to running pi window (teammate-send channel)");

console.log("\nPI-WINDOW E2E OK");

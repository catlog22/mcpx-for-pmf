// mcpx-for-pmf end-to-end verification (raw JSON-RPC over Streamable HTTP):
// 1) tools/list contains pi_execute
// 2) session open -> skill_tool list exposes the pi plugin skills
// 3) plan create -> pi_execute (companion-style, fast model) -> plan read shows completed + execute evidence
// Usage: node e2e-mcpx.mjs [endpoint] [workspace]
const endpoint = process.argv[2] ?? "http://127.0.0.1:9090/mcp";
const workspace = process.argv[3] ?? "mcpx-for-pmf";

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

// JSON-RPC notifications carry no id; servers may reject id-bearing notification bodies.
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
    // SSE: parse the last data block as the JSON-RPC payload.
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

const init = await rpc("initialize", {
  protocolVersion: "2025-11-25",
  capabilities: {},
  clientInfo: { name: "e2e-pmf", version: "1.0.0" },
});
console.log("initialize ok, server:", init.serverInfo?.name, init.serverInfo?.version);
await notify("notifications/initialized", {});

// 1) tools/list
const listed = await rpc("tools/list", {});
const names = (listed.tools ?? []).map((t) => t.name).sort();
console.log("tools:", names.join(", "));
if (!names.includes("pi_execute")) {
  console.error("FAIL: pi_execute not in tools/list");
  process.exit(1);
}
console.log("PASS: pi_execute registered");

// 2) session open, then skill_tool list
const opened = await rpc("tools/call", { name: "session", arguments: { action: "open", workspace } });
const openedText = textOf(opened);
const remoteId = openedText.match(/remote_session_id"?[: ]+"([^"]+)/)?.[1];
if (!remoteId) {
  console.error("FAIL: session open:", openedText.slice(0, 500));
  process.exit(1);
}
console.log("PASS: session open, remote_session_id =", remoteId);

const skills = await rpc("tools/call", {
  name: "skill_tool",
  arguments: { action: "list", remote_session_id: remoteId },
});
const skillText = textOf(skills);
const hasPiSkill = /maestro-companion|maestro-knowledge|team-swarm/.test(skillText);
console.log("skill_tool list mentions pi plugin skills:", hasPiSkill);
if (!hasPiSkill) {
  console.error("FAIL: pi plugin skills not discovered:", skillText.slice(0, 600));
  process.exit(1);
}
console.log("PASS: pi plugin skills discoverable via skill_tool");

// 3) plan create -> pi_execute -> plan read
const plan = await rpc("tools/call", {
  name: "plan",
  arguments: { action: "create", remote_session_id: remoteId, purpose: "e2e verify pi dispatch", summary: "e2e", tasks: [{ title: "pi ping" }] },
});
const planText = textOf(plan);
const planId = planText.match(/plan_id"?[: ]+"([^"]+)/)?.[1];
const taskId = planText.match(/plan_task_id"?[: ]+"([^"]+)/)?.[1];
if (!planId || !taskId) {
  console.error("FAIL: plan create:", planText.slice(0, 800));
  process.exit(1);
}
console.log("PASS: plan created:", planId, "task:", taskId);

console.log("dispatching pi_execute (runs the local Pi agent, ~10-60s)...");
const dispatch = await rpc("tools/call", {
  name: "pi_execute",
  arguments: {
    action: "run",
    remote_session_id: remoteId,
    purpose: "e2e verify local pi dispatch",
    prompt: "Reply with exactly: PONG",
    plan_id: planId,
    plan_task_id: taskId,
    model: "maestro-qwen--deepseek-v4-flash/deepseek-v4-flash",
    system: "e2e system injection check",
    yield_time_ms: 120000,
  },
});
const dispatchText = textOf(dispatch);
console.log(dispatchText.slice(0, 1200));
const completed = /completed_in_call"?[: ]+true|"status"?[: ]"completed"/.test(dispatchText);
const exit0 = /exit_code"?[: ]0/.test(dispatchText);
if (!completed || !exit0) {
  console.error("FAIL: pi_execute did not complete inline (exit 0):", dispatchText.slice(0, 2000));
  process.exit(1);
}
console.log("PASS: pi_execute completed inline with exit 0");

const readBack = await rpc("tools/call", {
  name: "plan",
  arguments: { action: "read", remote_session_id: remoteId, plan_id: planId },
});
const readText = textOf(readBack);
const taskCompleted = /"status"?[: ]"completed"/.test(readText);
const hasEvidence = /"kind"?[: ]"execute"/.test(readText) && /"reference_id"/.test(readText);
console.log("plan read: task completed =", taskCompleted, ", execute evidence =", hasEvidence);
if (!taskCompleted || !hasEvidence) {
  console.error("FAIL: plan task not closed with evidence:", readText.slice(0, 2000));
  process.exit(1);
}
console.log("PASS: plan task completed with execute evidence — full loop closed");

console.log("\nE2E OK");

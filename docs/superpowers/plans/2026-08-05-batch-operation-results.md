# 批量查询异步操作结果实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 扩展 `operation_manage`，让模型可以一次查询多个已有异步操作的状态或聚合结果，并通过错误提示避免把 `operation_manage` 嵌套到 `operation_batch`。

**架构：** 保留现有 30 个公开工具和单操作接口；在 `operation_manage` 中使用 `operation_id` / `operation_ids` 二选一选择单操作或批量模式。批量模式先完成全部存在性与 Remote Session 归属校验，再复用 `operation.Service.Get` / `Result` 读取数据，最后按操作状态聚合外层 ARC 状态。

**技术栈：** Go 1.26.1+、标准库 `testing`、MCP raw input schema、SQLite 持久化的 `internal/operation.Service`。

---

## 文件清单

- 修改：`internal/operation/service.go`——增加批量查询数量上限常量。
- 修改：`internal/server/tools_catalog.go`——扩展 `operation_manage` 的公开 schema、描述和批量分支。
- 修改：`internal/server/tools_operation.go`——解析批量目标、执行整批权限校验、组装 item、聚合外层状态，并更新嵌套错误引导。
- 修改：`internal/server/tools_operation_test.go`——增加批量状态、批量结果、分页、校验、权限和状态聚合测试。
- 修改：`internal/server/public_catalog_test.go`——验证公开工具数量不变且 schema 暴露单/批量互斥分支。
- 创建但不提交：`docs/superpowers/specs/2026-08-05-batch-operation-results-design.md`、本计划文件——设计与计划文档均被仓库忽略。

### 任务 1：先写批量查询行为测试

**文件：**

- 修改：`internal/server/tools_operation_test.go`
- 修改：`internal/server/public_catalog_test.go`

- [x] **步骤 1：增加确定性操作测试辅助函数**

在 `tools_operation_test.go` 增加一个使用 `rt.operations.Submit` 的辅助函数，避免批量测试依赖文件扫描时序：

```go
func submitOperationForTest(t *testing.T, rt *Runtime, session remotesession.Session, stepID, result string) operation.Record {

	t.Helper()
	record, err := rt.operations.Submit(context.Background(), operation.SubmitSpec{
		RemoteSessionID: session.ID,
		WorkspaceName:   session.WorkspaceName,
		RequestID:       "req_batch_test_" + stepID,
		Purpose:         "批量查询测试",
		Steps:           []operation.StepSpec{{ID: stepID, Tool: "source_read"}},
	}, func(context.Context, operation.ExecuteInput) operation.ExecuteResult {
		return operation.ExecuteResult{Result: []byte(result)}
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, timedOut, err := rt.operations.Wait(context.Background(), record.ID, 5*time.Second)
	if err != nil || timedOut || completed.State != operation.StateSucceeded {
		t.Fatalf("operation did not complete: record=%+v timedOut=%v err=%v", completed, timedOut, err)
	}
	return completed
}
```

- [x] **步骤 2：编写批量 status/result 与独立分页测试**

增加 `TestOperationManageBatchStatusAndResult`：提交两个成功操作，调用：

```go
map[string]any{
	"session_id": session.ID,
	"action":     "status",
	"operation_ids": []any{first.ID, second.ID},
}
```

断言外层为 `succeeded`、`data.items` 长度为 2、item 顺序与输入一致、每个 item 的 `operation_id` 和 `state` 正确。随后用相同 ID 调用 `action=result`，断言每个 item 的聚合 `result` 正确。

使用一个超过小 `limit` 的 JSON 结果调用批量 `result`，断言每个 item 都有自己的 `next_cursor`；再用其中一个 item 的游标通过现有单操作 `operation_id + cursor` 调用，确认原有分页行为仍可继续。

- [x] **步骤 3：编写参数、权限和嵌套引导测试**

增加表驱动测试覆盖以下输入，并断言 `status=failed`、错误消息包含对应字段：

```go
{name: "missing targets", args: map[string]any{"session_id": session.ID, "action": "status"}},
{name: "both target forms", args: map[string]any{"session_id": session.ID, "action": "status", "operation_id": "op-1", "operation_ids": []any{"op-2"}}},
{name: "empty operation_ids", args: map[string]any{"session_id": session.ID, "action": "status", "operation_ids": []any{}}},
{name: "duplicate operation_ids", args: map[string]any{"session_id": session.ID, "action": "status", "operation_ids": []any{"op-1", "op-1"}}},
{name: "batch wait", args: map[string]any{"session_id": session.ID, "action": "wait", "operation_ids": []any{"op-1"}}},
{name: "batch step", args: map[string]any{"session_id": session.ID, "action": "result", "operation_ids": []any{"op-1"}, "step_id": "step-1"}},
{name: "batch cursor", args: map[string]any{"session_id": session.ID, "action": "result", "operation_ids": []any{"op-1"}, "cursor": "10"}},
```

补充 33 个 ID 的上限测试；补充一批包含当前会话有效 ID 与不存在 ID 的测试，断言整批失败且没有 `data.items`；创建另一 Remote Session 并提交属于它的操作，断言跨会话批量读取返回 `forbidden` 且不返回部分结果。调用 `operation_batch` 嵌套 `operation_manage`，断言错误消息包含 `operation_ids` 和 `action=status/result`。

- [x] **步骤 4：编写外层状态聚合测试**

为服务端新增的纯函数准备状态表测试，明确优先级：

```go
tests := []struct {
	name   string
	states []operation.State
	want   envelope.Status
}{
	{"confirmation wins", []operation.State{operation.StateSucceeded, operation.StateWaitingConfirmation}, envelope.StatusNeedConfirmation},
	{"running after confirmation", []operation.State{operation.StateRunning, operation.StateSucceeded}, envelope.StatusAccepted},
	{"failure after terminal", []operation.State{operation.StateFailed, operation.StateCancelled}, envelope.StatusError},
	{"interrupted", []operation.State{operation.StateInterrupted, operation.StateCancelled}, envelope.StatusInterrupted},
	{"all success", []operation.State{operation.StateSucceeded, operation.StateSucceeded}, envelope.StatusOK},
}
```

- [x] **步骤 5：运行新增测试，确认在实现前失败**

运行：

```bash
rtk go test ./internal/server -run 'TestOperationManageBatch|TestOperationBatchStatus|TestPublicCatalog' -count=1
```

预期：因 `operation_ids` 尚未注册和批量分支尚未实现而失败；不得把失败当作实现完成。

### 任务 2：增加批量查询上限并实现运行时目标解析

**文件：**

- 修改：`internal/operation/service.go:17-23`
- 修改：`internal/server/tools_operation.go:13-165`

- [x] **步骤 1：增加命名上限常量**

在现有操作常量中增加：

```go
MaxBatchQueries = 32
```

服务端使用该常量而不是复制数字，避免查询批次上限与 DAG 步骤上限形成隐式耦合。

- [x] **步骤 2：实现严格的目标解析函数**

在 `tools_operation.go` 增加解析函数，区分字段缺失和空数组，支持 MCP JSON 解码得到的 `[]any`，并返回去空格后的 ID：

```go
func parseOperationTargets(payload map[string]any, action string) (ids []string, batch bool, err error) {
	operationID := strings.TrimSpace(stringPayload(payload, "operation_id"))
	rawIDs, hasIDs := payload["operation_ids"]
	if operationID != "" && hasIDs {
		return nil, false, errors.New("operation_id and operation_ids are mutually exclusive")
	}
	if hasIDs {
		ids, err = parseNonEmptyStringSlice(rawIDs, "operation_ids")
		if err != nil {
			return nil, false, err
		}
		if len(ids) == 0 || len(ids) > operation.MaxBatchQueries {
			return nil, false, fmt.Errorf("operation_ids must contain 1-%d IDs", operation.MaxBatchQueries)
		}
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if _, exists := seen[id]; exists {
				return nil, false, fmt.Errorf("operation_ids contains duplicate ID %q", id)
			}
			seen[id] = struct{}{}
		}
		if action != "status" && action != "result" {
			return nil, false, errors.New("batch operation_ids supports only action=status or action=result")
		}
		if strings.TrimSpace(stringPayload(payload, "step_id")) != "" || strings.TrimSpace(stringPayload(payload, "cursor")) != "" {
			return nil, false, errors.New("step_id and cursor require a single operation_id")
		}
		return ids, true, nil
	}
	if operationID == "" {
		return nil, false, errors.New("exactly one of operation_id or operation_ids is required")
	}
	return []string{operationID}, false, nil
}
```

`parseNonEmptyStringSlice` 对非 `[]any`、非字符串元素和空白元素返回字段名明确的 `bad_request` 错误；不修改已有 `depends_on` 解析器的错误文案。

- [x] **步骤 3：重排 `toolOperationManage` 的读取流程**

在取得 Remote Session、动作和 `r.operations` 后先调用目标解析；按输入顺序 `Get` 每个 ID，并在构造任何 item 前检查：

```go
records := make([]operation.Record, len(ids))
for index, id := range ids {
	record, err := r.operations.Get(ctx, id)
	if err != nil {
		return r.operationError(envReq, session, err)
	}
	if record.RemoteSessionID != session.ID {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "forbidden", "operation belongs to another Remote Session")
	}
	records[index] = record
}
if batch {
	return r.operationBatchManageResponse(ctx, envReq, session, action, records)
}
record := records[0]
```

保留单操作 `wait`、`cancel`、`resume` 分支原逻辑；单操作 `result` 继续把 `step_id`、`result`、`next_cursor` 放在原返回位置。

### 任务 3：实现批量 item 组装和 ARC 状态聚合

**文件：**

- 修改：`internal/server/tools_operation.go:174-224`

- [x] **步骤 1：抽取结果 item 组装函数**

复用 `operationView` 生成状态字段，新增结果 item 函数：

```go
func operationResultView(page operation.ResultPage) map[string]any {
	data := operationView(page.Operation, false)
	data["result"] = decodeJSONValue(page.Result)
	data["next_cursor"] = page.NextCursor
	return data
}
```

单操作 `result` 改用该函数后再补回既有的 `step_id` 字段，确保单操作返回兼容；批量结果调用时不传 `step_id`，只读取每个操作的聚合结果。

- [x] **步骤 2：实现批量读取和结果重新加载**

新增 `operationBatchManageResponse`：`status` 使用已校验的 records 直接组装；`result` 对每个 ID 调用 `r.operations.Result(ctx, id, "", "", intPayload(...))`，使用返回的 `page.Operation` 作为状态和视图来源，按输入顺序生成 `items`。返回数据必须包含 `action` 和 `items`，不能返回单操作顶层字段。

- [x] **步骤 3：实现外层状态聚合和响应构造**

新增纯函数 `operationBatchStatus(records []operation.Record) envelope.Status`，按确认、运行中、失败、中断、成功的优先级返回状态。新增响应函数使用与 `operationResponse` 相同的 ARC 构造方式：

```go
switch operationBatchStatus(records) {
case envelope.StatusAccepted:
	response = envelope.Accepted(envReq.RequestID, session.WorkspaceName, data)
case envelope.StatusNeedConfirmation:
	response = envelope.Fail(envelope.StatusNeedConfirmation, envReq.RequestID, session.WorkspaceName, data, "USER_CONFIRMATION_REQUIRED", "批量操作中存在等待语义确认的操作")
case envelope.StatusInterrupted:
	response = envelope.Interrupted(envReq.RequestID, session.WorkspaceName, data)
case envelope.StatusError:
	response = envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, data, "OPERATION_FAILED", "批量操作中存在执行失败的操作")
default:
	response = envelope.OK(envReq.RequestID, session.WorkspaceName, data)
}
response.RemoteSessionID = session.ID
return r.resultJSON(response)
```

### 任务 4：更新公开 schema 与模型引导

**文件：**

- 修改：`internal/server/tools_catalog.go:441-445`
- 修改：`internal/server/tools_operation.go:47-49`
- 修改：`internal/server/public_catalog_test.go`

- [x] **步骤 1：暴露 `operation_ids` 并建立 schema 互斥分支**

将当前 `operation_manage` 注册改为局部 `mcp.Tool`，保留 `publicTool` 的公开字段转换和 `additionalProperties=false`，再在 raw schema 增加两个 `oneOf` 分支：

```go
map[string]any{
	"type": "object",
	"properties": map[string]any{
		"action": map[string]any{"enum": []string{"status", "wait", "result", "cancel", "resume"}},
	},
	"required": []string{"operation_id"},
}
map[string]any{
	"type": "object",
	"properties": map[string]any{
		"action": map[string]any{"enum": []string{"status", "result"}},
	},
	"required": []string{"operation_ids"},
}
```

`operation_ids` 属性使用字符串数组 schema，并描述最大 32 个 ID；顶层 `required` 只保留 `session_id` 和 `action`，由 `oneOf` 表达目标二选一。注册描述明确写出“批量查询直接传 operation_ids，不要嵌套 operation_batch”。

- [x] **步骤 2：更新嵌套错误引导**

把 `toolOperationBatch` 中的错误文本改为：

```text
tool "operation_manage" cannot be nested in operation_batch; use operation_manage with action=status/result and operation_ids for batch queries
```

其他嵌套工具继续使用原有拒绝文案。

- [x] **步骤 3：增加 schema 契约断言**

在 `TestPublicCatalogIsExactlyTheV2Contract` 中读取 `operation_manage` raw schema，断言：工具总数仍为 30；`operation_ids` 为数组；顶层不再强制 `operation_id`；`oneOf` 同时存在单操作与批量分支，批量分支的动作只有 `status`、`result`。

### 任务 5：格式化并执行分层验证

**文件：**

- 修改：任务 1–4 中列出的 Go 文件。

- [x] **步骤 1：格式化实现文件**

运行：

```bash
rtk gofmt -w internal/operation/service.go internal/server/tools_catalog.go internal/server/tools_operation.go internal/server/tools_operation_test.go internal/server/public_catalog_test.go
```

- [x] **步骤 2：运行相关测试**

运行：

```bash
rtk go test ./internal/operation ./internal/server -count=1
```

预期：相关包测试全部通过，批量状态、结果、分页、schema、校验和权限用例均通过。

- [x] **步骤 3：运行全量验证**

依次运行：

```bash
rtk go test ./... -count=1
rtk go test -race ./... -count=1
rtk go vet ./...
test -z "$(gofmt -l ./cmd ./internal)"
rtk git diff --check
rtk go build -o bin/mcpx-server ./cmd/mcpx-server
```

预期：全量测试、竞态检测、静态检查、格式检查、差异检查和构建均成功；不提交任何 commit。

### 任务 6：验证并修复 Changeset digest 的模型可见性

**文件：**

- 检查并按根因修改：`internal/server/tools_changeset.go`、`internal/server/tools_change_execute.go`、`internal/server/tools_catalog.go`、`internal/arc/` 或负责终端摘要的观测适配文件。
- 增加回归测试：对应 Changeset/server/ARC 测试文件。

- [x] **步骤 1：复现并区分三层结果**

针对 `change_prepare -> change_read/change_apply` 流程分别检查：工具 handler 返回的结构化 `data`、ARC `mcpx.result` 载荷、终端观测摘要是否包含同一个完整 `sha256:<64 位十六进制>` digest；以结构化结果是否包含可原样复制的 `digest` 为模型可用性的验收依据，不以摘要文本代替。

- [x] **步骤 2：修复真实传播断点并补回归测试**

确保 `change_prepare` 成功返回的 `data.digest`、`change_read` 对同一 Changeset 的读取结果以及 `change_apply` 的冲突恢复详情使用同一个签发值；测试必须断言精确字符串相等，并断言摘要过长时不会截断 digest。

- [x] **步骤 3：验证 Changeset 流程**

运行：

```bash
rtk go test ./internal/server ./internal/arc -count=1
rtk go test ./... -count=1
```

预期：digest 回归测试和全量测试均通过，且不改变 `expected_digest` 必须原样复制的契约。

## 规格覆盖自检

- 批量 `status` / `result`：任务 1、2、3。
- 单/批量二选一、32 个上限、重复和空数组：任务 1、2、4。
- 整批存在性与 Remote Session 校验、禁止部分结果：任务 1、2。
- 批量不支持 `wait` / `cancel` / `resume`，步骤和游标保留单操作：任务 1、2。
- 每项独立结果游标：任务 1、3。
- 外层状态优先级：任务 1、3。
- 嵌套错误引导和模型描述：任务 1、4。
- 不新增工具、schema 互斥分支：任务 4。
- 单操作行为兼容：任务 1、3、5。
- Changeset digest 的结构化传播和终端可观测性：任务 6。

计划自检：全文无 TODO、待定或未定义的函数名；所有代码变更均绑定到具体文件、测试和命令；计划中的 `MaxBatchQueries`、`parseOperationTargets`、`operationResultView`、`operationBatchManageResponse`、`operationBatchStatus` 在前序任务中定义后再被后续任务使用；新增 digest 任务限定为批量查询完成后的传播验证，不改变已确认的批量接口范围。

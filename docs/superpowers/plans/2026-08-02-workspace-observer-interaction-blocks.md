# 工作区观测交互块与颜色 实现计划

> **面向 AI 代理的工作者：** 逐任务实现前必须使用 `subagent-driven-development`（推荐）或 `executing-plans`；步骤使用复选框（`- [ ]`）跟踪进度。

**目标：** 为 `mcpx-server workspace <name>` 的 text 输出增加按 MCP 工具调用归并的交互块、稳定动作颜色、Diff 增删背景色和 20 行正文预算，同时保持 JSON、事件持久化和非 TTY 行为兼容。

**架构：** 服务端沿用现有 `request_id` 作为交互关联键，并把它从带观测上下文的 Terminal Task 传到 `command.output`。`internal/observation` 提供有状态 `TextRenderer`，按事件 sequence 增量输出块；已有无状态 `RenderText` 保留。渲染器接收 `ColorMode` 和终端宽度，负责 24-bit Diff 背景、16 色降级、20 行正文预算、底部分隔线和块间空行。CLI 只负责检测终端颜色能力、创建渲染器、转发事件帧和处理 gap/错误帧。

**技术栈：** Go 标准库、`testing`、SQLite 既有 `observation_events.request_id` 字段、ANSI 16 色与 24-bit 真彩色控制码；不新增依赖或数据库列。

**提交政策：** 本仓库禁止 agent 自动 `commit` / `push`。每个任务完成后只运行验证并汇报变更摘要；如需提交，先展示完整摘要和 commit message，等待用户明确确认。

---

### 任务 1：为 Terminal Task 增加观测请求上下文

**文件：** 修改 `internal/terminal/task.go`；测试 `internal/terminal/task_observation_test.go`。

- [ ] **步骤 1：编写失败测试。** 新增 `TestTaskOutputSinkReportsObservationIdentity`，用带上下文的启动入口并断言每个 chunk 的字段：

```go
task, err := manager.StartRemoteWithObservation(
	context.Background(), "req_1", "command_execute",
	"rs_observer", "demo", t.TempDir(), "printf 'out'",
)
if err != nil { t.Fatal(err) }
if chunk.RequestID != "req_1" || chunk.Tool != "command_execute" {
	t.Fatalf("observation identity=%+v", chunk)
}
```

- [ ] **步骤 2：运行失败测试。**

```bash
go test ./internal/terminal -run TestTaskOutputSinkReportsObservationIdentity -count=1
```

预期：FAIL，因为启动入口和 `OutputChunk` 字段尚不存在。

- [ ] **步骤 3：实现上下文传递。** 在 `OutputChunk` 和 `Task` 增加请求 ID、来源工具字段；保留现有 `StartRemote`，新增：

```go
func (m *TaskManager) StartRemoteWithObservation(
	ctx context.Context,
	requestID, tool, remoteSessionID, workspaceName, workDir, command string,
) (*Task, error) {
	return m.start(ctx, requestID, tool, remoteSessionID, workspaceName, workDir, command)
}
```

内部启动函数和 `lockedWriter.Write` 从 Task 复制两个字段，普通任务继续传空值。

- [ ] **步骤 4：格式化并验证。**

```bash
gofmt -w internal/terminal/task.go internal/terminal/task_observation_test.go
go test ./internal/terminal -count=1
```

预期：`internal/terminal` 全部 PASS。

### 任务 2：传入命令和变更验证的请求上下文

**文件：** 修改 `internal/server/tools_command_execute.go`、`internal/server/tools_change_execute.go`；测试 `internal/server/observer_integration_test.go`。

- [ ] **步骤 1：添加失败断言。** 在命令输出观测集成测试中检查事件的 `RequestID` 等于工具请求 ID、`Tool` 等于 `command_execute`；为 change verification 增加同样的 `change_execute` 断言。

- [ ] **步骤 2：运行定向测试。**

```bash
go test ./internal/server -run 'TestObservationRecordsRuntimeTaskOutput|TestObservation.*Verify' -count=1
```

预期：FAIL，输出事件的请求 ID和来源工具为空。

- [ ] **步骤 3：接入两个任务创建路径。** `executeCommandTask` 使用：

```go
task, err := r.tasks.StartRemoteWithObservation(
	ctx, envReq.RequestID, "command_execute",
	remote.ID, remote.WorkspaceName, remote.WorkspacePath, command,
)
```

`runVerifySteps` 增加 `requestID string` 参数，调用方传入 `envReq.RequestID`，内部使用带观测入口并传 `change_execute`。普通任务调用不变。

- [ ] **步骤 4：验证服务端观测。**

```bash
gofmt -w internal/server/tools_command_execute.go internal/server/tools_change_execute.go internal/server/observer_integration_test.go
go test ./internal/server -run TestObservation -count=1
```

预期：Observation 测试全部 PASS。

### 任务 3：桥接命令输出事件的关联字段

**文件：** 修改 `internal/server/observation_bridge.go`；测试 `internal/server/observer_integration_test.go`。

- [ ] **步骤 1：先增加字段断言。** `observeTaskOutput` 产生的事件必须复制：

```go
RequestID:   chunk.RequestID,
Tool:        chunk.Tool,
OperationID: chunk.TaskID,
Stream:      chunk.Stream,
Offset:      chunk.Offset,
```

- [ ] **步骤 2：实现复制。** 保持 `SanitizeText` 和现有日志 URI；不把原始命令或未脱敏参数写入命令输出事件。

- [ ] **步骤 3：验证脱敏和字段。**

```bash
gofmt -w internal/server/observation_bridge.go
go test ./internal/server -run 'TestObservationRecordsRuntimeTaskOutput|TestObservationRecordsToolLifecycleAndRedacts' -count=1
```

预期：PASS，敏感值仍不会出现在观测输入、输出或命令输出事件中。

### 任务 4：实现颜色模式、Diff 行样式和渲染选项

**文件：** 创建 `internal/observation/palette.go`；修改 `internal/observation/render.go`；测试 `internal/observation/render_test.go`。

- [ ] **步骤 1：先写失败测试。** 覆盖工具颜色、Diff 前景/背景、颜色降级和渲染选项：

```go
func TestActionColorUsesToolAndErrorOverride(t *testing.T) {
	if got := actionColor("command_execute", false); got != ansiAmber {
		t.Fatalf("command color=%q", got)
	}
	if got := actionColor("file_read", true); got != ansiRed {
		t.Fatalf("error color=%q", got)
	}
}


func TestDiffLineStyleUsesTrueColorBackground(t *testing.T) {
	added := diffLineStyle("+new", ColorModeTrueColor)
	if !strings.Contains(added, ansiDiffAddedBackground) || !strings.Contains(added, ansiDiffAddedForeground) || !strings.HasSuffix(added, ansiReset) {
		t.Fatalf("added style=%q", added)
	}
	deleted := diffLineStyle("-old", ColorModeTrueColor)
	if !strings.Contains(deleted, ansiDiffRemovedBackground) || !strings.Contains(deleted, ansiDiffRemovedForeground) {
		t.Fatalf("deleted style=%q", deleted)
	}
	if header := diffLineStyle("+++ b/demo.go", ColorModeTrueColor); strings.Contains(header, ansiDiffAddedBackground) || !strings.Contains(header, ansiDiffAddedForeground) {
		t.Fatalf("added header style=%q", header)
	}
	if header := diffLineStyle("--- a/demo.go", ColorModeTrueColor); strings.Contains(header, ansiDiffRemovedBackground) || !strings.Contains(header, ansiDiffRemovedForeground) {
		t.Fatalf("removed header style=%q", header)
	}
	if got := diffLineStyle("+new", ColorModeANSI16); strings.Contains(got, "48;2;") {
		t.Fatalf("16-color style unexpectedly has truecolor background=%q", got)
	}
	if got := diffLineStyle("+new", ColorModeNone); got != "+new" {
		t.Fatalf("no-color style=%q", got)
	}
}

func TestInteractionBodyBudgetConstant(t *testing.T) {
	if maxInteractionBodyLines != 20 {
		t.Fatalf("body budget=%d", maxInteractionBodyLines)
	}
}
```

- [ ] **步骤 2：运行失败测试。**

```bash
go test ./internal/observation -run 'TestActionColor|TestDiffLineStyle|TestInteractionBodyBudget' -count=1
```

预期：FAIL，因为颜色模式、Diff 样式函数和新的正文预算尚未实现。

- [ ] **步骤 3：实现颜色模式和固定调色板。** 在 `palette.go` 增加颜色模式，并保持工具颜色不依赖原始命令：

```go
type ColorMode uint8

const (
	ColorModeNone ColorMode = iota
	ColorModeANSI16
	ColorModeTrueColor
)

const (
	ansiAmber = "\033[33m"
	ansiCyan = "\033[36m"
	ansiBlue = "\033[34m"
	ansiGreen = "\033[32m"
	ansiMagenta = "\033[35m"
	ansiRed = "\033[31m"
	ansiYellow = "\033[33m"
	ansiDiffAddedForeground = "\033[38;2;103;232;160m"
	ansiDiffAddedBackground = "\033[48;2;24;58;42m"
	ansiDiffRemovedForeground = "\033[38;2;255;143;143m"
	ansiDiffRemovedBackground = "\033[48;2;59;32;37m"
)

func actionColor(tool string, failed bool) string {
	if failed {
		return ansiRed
	}
	switch tool {
	case "command_execute":
		return ansiAmber
	case "context_query":
		return ansiCyan
	case "file_read":
		return ansiBlue
	case "change_execute", "file.changed":
		return ansiGreen
	case "session_open", "workspace_list":
		return ansiMagenta
	default:
		return ansiBlue
	}
}

func diffLineStyle(value string, mode ColorMode) string {
	if mode == ColorModeNone {
		return value
	}
	if strings.HasPrefix(value, "+++") {
		return diffHeaderStyle(ansiDiffAddedForeground, value)
	}
	if strings.HasPrefix(value, "---") {
		return diffHeaderStyle(ansiDiffRemovedForeground, value)
	}
	if mode == ColorModeTrueColor && strings.HasPrefix(value, "+") {
		return ansiDiffAddedBackground + ansiDiffAddedForeground + value + ansiReset
	}
	if mode == ColorModeTrueColor && strings.HasPrefix(value, "-") {
		return ansiDiffRemovedBackground + ansiDiffRemovedForeground + value + ansiReset
	}
	if strings.HasPrefix(value, "+") {
		return ansiGreen + value + ansiReset
	}
	if strings.HasPrefix(value, "-") {
		return ansiRed + value + ansiReset
	}
	return value
}

func diffHeaderStyle(foreground, value string) string {
	return foreground + value + ansiReset
}
```

`+++` 和 `---` 只使用前景色，不误判为新增/删除内容；`diffLineStyle` 必须检查三字符头部后再检查单字符前缀。每一行都追加 `ansiReset`。文本渲染增加 `renderOptions{colorMode ColorMode, terminalWidth int}`，`RenderText` 保留现有 `bool` 包装并映射到 `ColorModeANSI16`，有状态渲染器使用完整选项。Diff 背景填充在渲染选项提供的可视宽度内完成，不能把 ANSI 控制符算入宽度；JSON 函数和事件字段保留。

```go
type renderOptions struct {
	colorMode             ColorMode
	terminalWidth         int
	suppressCommandOutput bool
}

func RenderText(w io.Writer, event Event, color bool) error {
	mode := ColorModeNone
	if color {
		mode = ColorModeANSI16
	}
	return renderTextWithOptions(w, event, renderOptions{colorMode: mode})
}

func renderTextWithOptions(w io.Writer, event Event, options renderOptions) error
```

`renderFileChanged` 通过 `renderOptions` 调用 Diff 行格式化函数；有状态渲染器传入真实终端宽度，旧无状态入口继续使用默认宽度和 ANSI16。

- [ ] **步骤 4：运行 observation 测试。**

```bash
gofmt -w internal/observation/palette.go internal/observation/render.go internal/observation/render_test.go
go test ./internal/observation -count=1
```

预期：PASS；旧 Diff 测试改为验证 URI 不出现在 text 中。

### 任务 5：实现有状态 TextRenderer、20 行正文预算和块间空行

**文件：** 创建 `internal/observation/timeline.go`、`internal/observation/timeline_test.go`。

- [ ] **步骤 1：编写失败测试。** 直接构造同包 `interactionBlock`，验证正文预算、边界线和空行的独立计数：

```go
func TestInteractionBodyBudgetExcludesBordersAndAddsBlankLine(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTextRendererWithMode(ColorModeNone, 80)
	block := &interactionBlock{sequence: 1, tool: "file_read"}
	renderer.blocks["test"] = block
	if err := renderer.activate(&output, block); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 21; index++ {
		if err := renderer.writeBodyLine(&output, block, fmt.Sprintf("line-%02d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := renderer.close(&output, block); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Count(text, "│ ") != 20 || !strings.Contains(text, "│ ...") {
		t.Fatalf("body budget output=%q", text)
	}
	if !strings.HasSuffix(text, "\n\n") {
		t.Fatalf("missing blank line after footer=%q", text)
	}
}

func TestInteractionLineBudget(t *testing.T) {
	if maxInteractionBodyLines != 20 {
		t.Fatalf("body budget=%d", maxInteractionBodyLines)
	}
}
```

- [ ] **步骤 2：运行失败测试。**

```bash
go test ./internal/observation -run 'TestInteractionBodyBudget|TestInteractionLineBudget' -count=1
```

预期：FAIL，因为正文预算仍未改为 20，关闭块时还没有追加底部分隔线后的空行。

- [ ] **步骤 3：实现正文预算和边界输出。** 将 `maxInteractionBodyLines` 改为 20，移除“顶部/底线计入总预算”的逻辑。为 `interactionBlock` 增加待输出的最后一行，避免流式事件在刚好 20 行时错误输出 `...`：

```go
type interactionBlock struct {
	key           string
	sequence      int64
	tool          string
	failed        bool
	continuation  bool
	opened        bool
	closed        bool
	bodyLines     int
	pendingLine   string
	ellipsis      bool
	commandOutput bool
}

func (r *TextRenderer) close(w io.Writer, block *interactionBlock) error {
	if block.pendingLine != "" && !block.ellipsis {
		if err := r.flushBodyLine(w, block, block.pendingLine); err != nil {
			return err
		}
		block.pendingLine = ""
	}
	footer := "╰" + strings.Repeat("─", r.width-1)
	if _, err := fmt.Fprintln(w, paint(footer, actionColor(block.tool, block.failed), r.colorMode != ColorModeNone)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	block.closed = true
	return nil
}
```

`writeBodyLine` 在收到下一行时先冲刷上一行；当已有 19 行正文且再次收到内容时丢弃待输出的第 20 行并写入唯一的 `...`。如果没有溢出，`close` 冲刷待输出行，因此恰好 20 行正文不会产生 `...`。顶部和底部分隔线使用动作颜色但不增加 `bodyLines`。

- [ ] **步骤 4：实现接口和状态。** 保留旧构造函数兼容调用，并增加带颜色模式的构造函数：

```go
type TextRenderer struct {
	colorMode ColorMode
	width     int
	blocks    map[string]*interactionBlock
	activeKey string
	fallbackSeq uint64
}

func NewTextRenderer(color bool) *TextRenderer
func NewTextRendererWithMode(mode ColorMode, terminalWidth int) *TextRenderer
func (r *TextRenderer) RenderEvent(w io.Writer, event Event) error
func (r *TextRenderer) ResetAfterGap()
```

事件键按 `request_id`、`operation_id`、`sequence` 依次降级；状态记录工具、颜色、已用行数、是否写过 `...` 和关闭状态。`tool.started` 只建立状态，首次可见事件写顶部边界，关闭时写 footer。

- [ ] **步骤 5：接入摘要、Diff 样式、颜色和去重。** 将现有三个 render 函数产生的逻辑行交给预算器；调用带 `renderOptions` 的渲染路径，使 Diff 行背景填充到正文可视宽度并在截断后追加 Reset；命令完成事件若已输出相同请求的 `command.output`，只保留完成摘要，避免重复 stdout/stderr。

- [ ] **步骤 6：运行测试。**

```bash
gofmt -w internal/observation/timeline.go internal/observation/timeline_test.go
go test ./internal/observation -count=1
```

预期：旧渲染测试和新增交互块测试全部 PASS。

### 任务 6：接入 workspace CLI

**文件：** 修改 `cmd/mcpx-server/workspace.go`；测试 `cmd/mcpx-server/workspace_test.go`。

- [ ] **步骤 1：添加连续 frame 和颜色能力测试。** 同一 request 只创建一个 text 块；JSON frame 仍直接调用 `RenderJSON`；gap 后不复用旧关联；验证 `NO_COLOR` 返回 `ColorModeNone`，`COLORTERM=truecolor` 或 `24bit` 返回 `ColorModeTrueColor`，普通 TTY 返回 `ColorModeANSI16`。

- [ ] **步骤 2：在 `runWorkspaceObserver` 中检测颜色能力并创建一次 renderer。** 保留非 TTY 和 `NO_COLOR` 的禁色行为：

```go
func terminalColorMode(isTTY bool, noColor, colorTerm string) observation.ColorMode {
	if !isTTY || noColor != "" {
		return observation.ColorModeNone
	}
	if strings.EqualFold(colorTerm, "truecolor") || strings.EqualFold(colorTerm, "24bit") {
		return observation.ColorModeTrueColor
	}
	return observation.ColorModeANSI16
}

var textRenderer *observation.TextRenderer
if options.Format == "text" {
	mode := terminalColorMode(stdoutIsTTY(), os.Getenv("NO_COLOR"), os.Getenv("COLORTERM"))
	textRenderer = observation.NewTextRendererWithMode(mode, terminalColumns())
}
```

事件调用 `textRenderer.RenderEvent`；gap 先输出重连摘要，再调用 `ResetAfterGap`。保留 `renderWorkspaceFrame` 和 `RenderText` 的兼容包装，避免 JSON 和既有单事件调用改变。

- [ ] **步骤 3：格式化并测试。**

```bash
gofmt -w cmd/mcpx-server/workspace.go cmd/mcpx-server/workspace_test.go
go test ./cmd/mcpx-server -count=1
```

预期：PASS，text 使用块渲染，JSON 仍一事件一行。

### 任务 7：更新观测文档

**文件：** 修改 `README.md:139-207`。

- [ ] **步骤 1：更新示例和说明。** 使用交互块和 Diff 示例，说明颜色仅 TTY、`NO_COLOR` 可关闭、支持真彩色/16 色降级、正文最多 20 个逻辑行，顶部/底部分隔线不计入预算，超出显示 `...`，底线下保留空行，text 不显示截断解释或 Resource URI。

- [ ] **步骤 2：检查文档一致性。**

```bash
rg -n 'workspace|20|Resource URI|NO_COLOR|COLORTERM|format json|truecolor' README.md | sed -n '1,160p'
```

预期：参数、text/json 行为、颜色降级和 20 行正文预算一致。

### 任务 8：完成验证和交付检查点

**文件：** 检查 `cmd/mcpx-server/`、`internal/observation/`、`internal/server/`、`internal/terminal/`。

- [ ] **步骤 1：运行改动包测试。**

```bash
go test ./cmd/mcpx-server ./internal/observation ./internal/server ./internal/terminal -count=1
```

预期：全部 PASS。

- [ ] **步骤 2：运行格式和静态检查。**

```bash
test -z "$(gofmt -l ./cmd ./internal)"
go vet ./...
```

预期：格式命令无输出，`go vet` 返回 0。

- [ ] **步骤 3：运行仓库级回归和竞态测试。**

```bash
go test ./... -count=1
go test -race ./... -count=1
```

预期：全部 PASS；竞态失败时记录具体包、测试和复现命令。

- [ ] **步骤 4：汇报变更和提交建议。** 汇报涉及文件、测试结果、兼容性和 `.superpowers/` 视觉原型未跟踪状态。不得自动执行 `git commit` 或 `git push`；如需提交，先展示摘要和完整 Conventional Commit message，等待明确确认。

## 执行记录

- 任务 1：完成 Task/OutputChunk 的 RequestID、Tool 传播，并修复输出测试的时序不稳定。
- 任务 2：核验命令任务和变更验证任务已使用带观测上下文的启动入口。
- 任务 3：完成命令输出事件字段桥接、跨 chunk 脱敏和 Bearer 脱敏测试。
- 任务 4–5：完成颜色模式、Diff 样式、20 行正文预算、`...` 保留位、彩色底线和底线后空行。
- 任务 6–7：完成 CLI 颜色能力检测和 README 说明同步。
- 任务 8：`go test ./... -count=1`、`go test -race ./... -count=1`、`gofmt`、`go vet` 和构建均通过。
- 交付策略：遵循仓库规则，未自动执行 commit 或 push。

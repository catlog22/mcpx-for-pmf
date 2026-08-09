# 面向客户端模型的高效 Changeset 交互整改计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（- [ ]）语法跟踪进度。

**目标：** 让客户端模型在文件变更中能够从一次明确结果继续执行，消除无状态重复读取和整批盲重试；常规单文件变更稳定为 source_read -> change_prepare -> change_apply 三次调用。

**架构：** 整改分为两个边界。MCPX 负责提供可直接决策的历史状态、结构化失败定位和适合终端观察的摘要；客户端负责以逻辑变更为单位维护有限状态机、缓存未变化的读取结果，并按服务端返回的失败文件局部恢复。两端通过 ARC data 与 recovery 契约协作，不能依赖终端折叠文本或反复读取历史猜状态。

**技术栈：** Go 1.26.1+、标准库 testing、MCP raw input schema、ARC、SQLite 状态库，以及仓库外 MCP 客户端调度层。

---

## 问题基线与验收目标

会话 eda58840-88ec-4db3-801c-081be92d41d4 证明问题不是网络重试：#5885-#5939 是 28 个不同 request_id 的 change_read(history, limit=1)，期间没有写操作，且每次返回同一个 applied Changeset。随后同一逻辑变更经历 24、12、12、7、7、7、5 个 operation 的连续 change_prepare，错误从 PATCH_TOO_MANY_FILES 漂移到 STALE_REVISION、hunk 行数和上下文不匹配。

| 场景 | 当前表现 | 首期目标 |
|---|---:|---:|
| 无写操作期间相同 change_read(history) | 28 次 | 0 次重复；每个逻辑变更最多 1 次，仅限审计或中断恢复 |
| 已读单文件的小型修改 | 多次检查后再准备 | 3 次调用；需要语义确认时为 4 次 |
| 相同逻辑变更的草稿 | 可产生重复 draft | 相同 payload 和 idempotency_key 只产生 1 个 Changeset |
| 已放弃的 draft | 无丢弃入口，计划交付被会话内任意 draft 阻塞 | 可审计地 discard；仅计划引用的 draft 阻塞交付，其余作为告警 |
| STALE_REVISION 恢复 | 重提整批 operation | 仅读取 failed_path，重建该 path 的 operation；相同失败最多 2 次 |
| 可编辑文件格式 | 客户端从差异表象猜测编码与换行 | 所有读取模式返回统一 format；Changeset 校验并回显格式是否保持 |
| 终端 history 展示 | data=object meta=object | 显示草稿数、最近 Changeset ID、状态和路径数 |

## 文件清单

- 修改：internal/changeset/service.go - 将逐 operation 的准备错误改为可 errors.As 读取的带索引、路径和操作类型的错误。
- 修改：internal/changeset/service_test.go - 验证错误上下文不丢失 ErrStaleRevision。
- 修改：internal/file/read.go - 统一检测源文件字符集、BOM、换行统计和末尾换行，并让窗口与完整读取共享格式元数据。
- 修改：internal/file/file_test.go - 覆盖 UTF-8 BOM、UTF-16 BOM、CRLF、混合换行与无末尾换行。
- 修改：internal/server/tools_source_unified.go - 在 full、window 和 batch 结果中返回相同的 format 对象，并在文本摘要中展示关键格式信息。
- 修改：internal/server/tools_source_unified_test.go - 覆盖三种读取形态的 format 一致性和显示结果。
- 修改：internal/server/tools_changeset.go - 构造 history DTO；将文件上限和逐 operation 失败写入稳定 details 和精确恢复动作。
- 修改：internal/server/tools_change_recovery_test.go - 覆盖 history 摘要、文件上限、失败 operation 定位和 source_read 恢复。
- 修改：internal/plan/delivery.go - 仅将计划证据引用的未应用 Changeset 作为交付阻塞，其余 draft 作为可清理告警。
- 修改：internal/plan/delivery_test.go - 覆盖引用 draft 阻塞、无关 draft 不阻塞和 discard 后交付。
- 修改：internal/server/agent_guidance.go - 增加避免 history 预检循环、使用业务幂等键和局部恢复规则；版本 1.16 升至 1.17。
- 修改：internal/server/agent_guidance_test.go - 断言新规则和 schema 描述可见。
- 修改：internal/server/tools_catalog.go - 明确 change_prepare.idempotency_key 和 change_read 的工作流用途。
- 修改：internal/observation/render.go - 为 change_read(history) 和 Changeset 失败增加专用终端摘要。
- 修改：internal/observation/render_test.go - 覆盖 history 成功、无草稿和单文件失败文本。
- 修改：internal/server/acceptance_protocol_test.go - 验证公开三调用成功路径和局部恢复契约。
- 创建但不提交：docs/superpowers/specs/2026-08-06-model-friendly-change-contract.md - 记录 MCPX 与 MCP 客户端之间的状态机、字段、重试上限和指标。
- 创建但不提交：本计划文件。docs/superpowers 已被忽略，不纳入提交。

## 客户端工作流契约

客户端实现不在当前仓库，不能在 MCPX 中伪造其文件路径。客户端负责人必须按以下状态机实现；MCPX 任务完成后，以本节作为联调输入。

    source_ready
      -> change_prepare(idempotency_key)
      -> draft_ready
      -> change_apply(changeset_id, expected_digest)
      -> applied | waiting_confirmation | blocked

    prepare_failed(PATCH_TOO_MANY_FILES)
      -> 按 max_patch_files 分批
      -> change_prepare(新的批次 idempotency_key)

    prepare_failed(STALE_REVISION | PATCH_CONTEXT_* | PATCH_HUNKS_OVERLAP)
      -> source_read(view=file, mode=full, path=failed_path, include_sha256=true)
      -> 仅重建 failed_path 的 operation
      -> change_prepare(仅受影响文件或独立批次)

客户端状态至少保存 session_id、logical_change_id、每个 path 的 sha256、成功读取指纹、changeset_id、expected_digest，以及按 path、base_sha256、error_code 计数的恢复次数。

每个成功的可编辑文件读取必须携带完整格式基线。既有 `encoding` 只表示内容传输编码（`utf-8` 或 `base64`），不得把它误当作源文件字符集；新增并固定返回：

    format = {
      charset: "utf-8" | "utf-16le" | "utf-16be" | "unknown",
      bom: "none" | "utf-8" | "utf-16le" | "utf-16be" | "unknown",
      line_ending: "LF" | "CRLF" | "CR" | "mixed" | "none",
      line_ending_counts: { lf, crlf, cr },
      final_newline: true | false | null,
    }

format 必须根据原始字节检测，window、full、batch 的 `results[]` 与后续读取均返回同一对象。非文本或无法可靠识别时返回 `charset=unknown`、`final_newline=null`，客户端不得猜测或把 base64 内容当 UTF-8 修改。客户端以 `path + sha256` 缓存 format，生成 `match`、`replacement` 和 Unified Diff 时严格保留 BOM、换行和末尾换行。每个 prepared 文件还必须回显 `original_format`、`proposed_format`、`format_preserved`；除用户明确要求格式化外，`format_preserved=false` 返回 `FORMAT_CHANGED` 且不创建草稿。

读取去重指纹为 tool + session_id + view + path/items + query + revision，排除 purpose、progress_summary、请求 ID 和追踪 ID。成功读取的指纹命中且期间没有 change_apply、file.changed 或新的文件 SHA 时，客户端复用缓存结果，不发送 MCP 调用。相同失败键达到 2 次时进入 blocked，呈现失败 path、错误码、当前 SHA 和下一步，而不是第三次提交相同 operation。

change_read(history) 只允许用于用户要求的审计、恢复未知中断状态或展示现有草稿；它不得作为 change_prepare 的常规前置条件。change_prepare 成功后，客户端直接使用返回的 changeset_id、expected_digest 和 next_action，不再调用 change_read(diff)，除非用户要求完整差异审阅。

当客户端决定不应用某个已知 draft（准备新草稿替代、失败恢复已拆分批次或用户取消）时，直接调用 `change_discard(changeset_id, reason)`，不通过 history 搜索。discard 只改变服务端草稿生命周期并保留审计记录，不改工作区；`change_revert` 仍只处理已应用 Changeset。Plan 交付默认只阻塞其证据引用但未应用的 Changeset，未引用的 draft 以 `orphan_drafts` 告警返回；需要“会话内无任何 draft”时由用户显式开启严格策略。

### 任务 1：冻结模型友好交互契约与回放样本

**文件：**

- 创建：docs/superpowers/specs/2026-08-06-model-friendly-change-contract.md
- 修改：internal/server/acceptance_protocol_test.go

- [ ] **步骤 1：写入状态机和调用预算规格**

在设计文档中记录以下不可协商规则：

    普通单文件精确修改：source_read(file/full) -> change_prepare -> change_apply
    history：仅审计或中断恢复；不能作为 prepare 前置检查
    prepare 成功：只能继续 apply 或等待确认
    prepare 失败：只能按 error.details.failed_path 或 max_patch_files 重建
    重复上限：同一 read 指纹 1 次；同一失败键 2 次

定义字段：history.pending_draft_count、history.latest_changeset、error.details.failed_operation_index、error.details.failed_path、error.details.failed_operation、error.details.current_recovery、error.details.max_patch_files 和 error.details.operation_count。next_action 只有在参数完整可直接调用时才出现；仅说明策略的内容使用 recovery_plan，不能伪装成可执行调用。

- [ ] **步骤 2：增加公开协议的三调用成功测试**

在 acceptance_protocol_test.go 复用现有 session fixture，准备 demo.go 的局部 replace_exact。测试严格执行：

    source_read(session_id, view=file, path=demo.go, mode=full, include_sha256=true)
    change_prepare(session_id, purpose, idempotency_key=logical-change-demo-value-v1,
      operations=[replace_exact(path=demo.go, base_sha256=sourceSHA,
      match="Value = 1", replacement="Value = 2")])
    change_apply(session_id, purpose, changeset_id=preparedID, expected_digest=preparedDigest)

断言三步均 succeeded、准备结果 next_action.tool 为 change_apply、应用后文件内容为 Value = 2。测试中不得调用 change_read(history) 或 change_read(diff)。

- [ ] **步骤 3：增加幂等草稿回放测试**

对步骤 2 的完全相同 change_prepare payload 再调用一次，保持 idempotency_key 不变。断言返回同一 changeset_id、idempotent_replay=true，并查询 changesets 表确认只有一个对应 draft；不得通过 history 查找草稿。

- [ ] **步骤 4：运行协议测试确认新增断言失败**

运行：

    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./internal/server -run 'TestAcceptance.*Change|Test.*Idempotent' -count=1

预期：新增 history 和失败细节断言在实现前失败；既有兼容性测试可以通过。

### 任务 2：在 Changeset 服务中保留失败 operation 身份

**文件：**

- 修改：internal/changeset/service.go
- 修改：internal/changeset/service_test.go

- [ ] **步骤 1：编写逐 operation 错误测试**

在 service_test.go 创建内容为 old\n 的 stale.txt，传入错误 base_sha256，调用 PrepareWithOptions。断言 errors.As(err, &operationErr) 为真，且 operationErr.Index=0、Path=stale.txt、Operation=replace_exact；再断言 errors.Is(err, ErrStaleRevision) 为真。

- [ ] **步骤 2：运行测试确认失败**

运行：

    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./internal/changeset -run TestPrepareReturnsOperationError -count=1

预期：FAIL，当前服务只返回字符串形式的 operation %d: 错误。

- [ ] **步骤 3：定义并使用可解包错误类型**

在 service.go 的错误定义附近新增：

    type OperationError struct {
        Index     int
        Path      string
        Operation string
        Err       error
    }

    func (e *OperationError) Error() string {
        return fmt.Sprintf("operation %d (%s %s): %v", e.Index, e.Operation, e.Path, e.Err)
    }

    func (e *OperationError) Unwrap() error { return e.Err }

在 buildChangeset 中，prepareOperation 与 options.Transform 的失败改为返回 OperationError。保留既有错误的 Unwrap 链，不能再靠字符串解析确定失败文件。

- [ ] **步骤 4：运行 Changeset 包测试**

运行：

    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./internal/changeset -count=1

预期：PASS，既有 stale revision、精确编辑、幂等和回滚测试均通过。

### 任务 3：把失败恢复变成单文件、可执行的契约

**文件：**

- 修改：internal/server/tools_changeset.go
- 修改：internal/server/tools_change_recovery_test.go

- [ ] **步骤 1：为恢复详情编写失败测试**

构造两个 operation：第一个有效，第二个对 stale.txt 使用过期 SHA。解码 ARC data.error.details，断言：

    failed_operation_index = 1
    failed_path = "stale.txt"
    failed_operation = "replace_exact"
    current_recovery = "read_failed_path_and_rebuild_only_that_operation"

断言 next_action 为 source_read，arguments 同时包含 view=file、path=stale.txt、mode=full、include_sha256=true 和 session_id。再增加 hunk 行数错误测试，断言同样返回 index/path，且不再生成 view=list 的模糊恢复动作。

- [ ] **步骤 2：为文件上限编写失败测试**

配置 MaxPatchFiles=2，提交 3 个不同 path 的 operation。断言 PATCH_TOO_MANY_FILES details 包含 operation_count=3、max_patch_files=2、recommended_changeset_count=2，且 recovery_plan.strategy=split_by_distinct_path、max_paths_per_changeset=2。断言错误不提供缺少 operations 的伪 change_prepare 或 change_apply 调用。

- [ ] **步骤 3：实现 typed error 到公开 details 的映射**

在 changeError 中通过 errors.As 提取 changeset.OperationError，统一写入失败 index、path、operation 和 current_recovery。对于 STALE_REVISION、PATCH_CONTEXT_NOT_FOUND、PATCH_CONTEXT_AMBIGUOUS 和 PATCH_HUNKS_OVERLAP，构造唯一恢复动作：

    source_read(
      session_id=remoteSessionID,
      view=file,
      path=operationErr.Path,
      mode=full,
      include_sha256=true,
    )

只在旧错误无法获得 typed path 时保留 source_read(view=list) 回退，并设置 current_recovery=locate_failed_path。将 MaxPatchFiles 检查抽为 helper，生成文件上限和 recovery_plan，删除现有不完整 next_actions。

- [ ] **步骤 4：运行恢复测试**

运行：

    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./internal/server -run 'TestChangePrepare.*(Stale|Hunk|Patch|Recovery)|TestChange.*Recovery' -count=1

预期：PASS；每一个失败响应都能定位 operation，或明确标记为无法定位。


### 任务 4：让 history 在模型和终端中直接表达状态

**文件：**

- 修改：internal/server/tools_changeset.go
- 修改：internal/server/tools_change_recovery_test.go
- 修改：internal/observation/render.go
- 修改：internal/observation/render_test.go

- [ ] **步骤 1：为 history DTO 和终端摘要编写测试**

创建一个 applied Changeset 和一个 draft，调用 change_read(session_id, view=history, limit=5)。断言 ARC data 包含 changesets、pending_draft_count=1；latest_changeset 同时包含非空 changeset_id、status=draft 和 file_count。

在 render_test.go 传入同类规范化 change_read 完成事件，断言文本包含“草稿 1 个”、最近 Changeset ID 和 draft。再传入无草稿历史，断言包含“无待应用草稿”，并且不包含 data=object 或 meta=object。

- [ ] **步骤 2：运行测试确认失败**

运行：

    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./internal/server ./internal/observation -run 'TestChangeHistory|TestRender.*Change' -count=1

预期：FAIL，当前 history 没有状态字段，观察渲染没有 change_read 专用摘要。

- [ ] **步骤 3：实现 history 状态 DTO 且保留旧文本 Envelope**

在 tools_changeset.go 增加 changeHistoryDTO(history []changeset.Changeset) map[string]any。它保留完整 changesets，并额外计算：

    data := map[string]any{
        "changesets": history,
        "pending_draft_count": draftCount,
        "latest_changeset": latest,
        "history_state": map[string]any{
            "has_pending_draft": draftCount > 0,
            "read_only": true,
            "prepare_preflight_required": false,
        },
    }

latest_changeset 仅包含 changeset_id、status、summary、digest、created_at 和 file_count，不能重复嵌入 diff。toolChangeHistory 继续通过 remoteResult 保留既有文本 Envelope，同时把 DTO 赋给 result.StructuredContent，让 ARC 与观测层得到同一份可解析数据。

- [ ] **步骤 4：实现专用观测摘要**

在 render.go 的 remoteDataSummary 或 structuredToolOutputSummary 中增加 change_read 分支。history 摘要固定为：

    Changeset 历史：草稿 1 个；最近 chg_xxx（draft，2 个文件）。
    Changeset 历史：无待应用草稿；最近 chg_xxx（applied，1 个文件）。

读取 diff 时保留既有 diff 渲染，不用 history 摘要覆盖它。摘要只读取规范化字段，不输出完整 diff、哈希或内部 meta 对象。

- [ ] **步骤 5：运行服务端与观测测试**

运行：

    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./internal/server ./internal/observation -run 'TestChangeHistory|TestRender.*Change' -count=1

预期：PASS；终端不再出现 data=object meta=object。

### 任务 5：把无循环工作流写入模型指导和工具 schema

**文件：**

- 修改：internal/server/agent_guidance.go
- 修改：internal/server/agent_guidance_test.go
- 修改：internal/server/tools_catalog.go

- [ ] **步骤 1：编写可见指导和 schema 测试**

断言 agentGuidanceInstructions() 同时包含以下完整语义：

    change_read(history) 不是 change_prepare 的常规前置条件
    同一成功读取结果在文件版本未变化前只能使用一次
    change_prepare 必须携带当前逻辑变更稳定的 idempotency_key
    STALE_REVISION、PATCH_CONTEXT_* 或 PATCH_HUNKS_OVERLAP 只重读 failed_path 并只重建该 operation
    同一 failed_path、base_sha256 和 error_code 连续失败两次后停止重试并报告阻塞

序列化 change_prepare schema，断言 idempotency_key 描述包含“相同逻辑变更”和“首次成功后不可更换 payload”。序列化 change_read schema，断言描述包含“审计或中断恢复”。

- [ ] **步骤 2：运行测试确认失败**

运行：

    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./internal/server -run 'TestAgentGuidance|TestChangeSchemas' -count=1

预期：FAIL，当前 1.16 指导没有循环上限、history 用途和局部失败恢复规则。

- [ ] **步骤 3：更新指导和公开工具描述**

将 agentGuidanceVersion 改为 1.17。在文件变更规则附近加入步骤 1 的五条约束，并明确成功 change_prepare 后直接使用其 next_action；除用户要求完整审阅外不得立即 change_read(diff)。

将 change_prepare.idempotency_key 的描述改为：

    同一逻辑变更的稳定业务幂等键；网络或客户端重试必须原样复用。首次准备成功后不得使用同一键提交不同 operations。

将 change_read 的工具描述改为：

    读取已知 Changeset 的差异或进行审计/中断恢复；不能作为每次 change_prepare 前的重复状态检查。

- [ ] **步骤 4：运行指导、目录和公开协议测试**

运行：

    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./internal/server -run 'TestAgentGuidance|TestChangeSchemas|TestPublicCatalog|TestAcceptance' -count=1

预期：PASS；session_open 返回的指导版本同步为 1.17。

### 任务 6：在 MCP 客户端实现状态机、缓存与局部重试

**范围：** 当前 MCPX 仓库外；客户端负责人必须在会话调度层实现，不能把控制逻辑下沉为 prompt 文案。

- [ ] **步骤 1：定义逻辑变更状态对象和指纹函数**

客户端创建 logical_change_id，持久化 session_id、path 到 sha256 的映射、成功读取缓存、prepared changeset_id/expected_digest 和失败计数。读取指纹只包含工具、session、视图、path/items、query 和 revision；purpose、progress_summary、请求 ID 与追踪 ID 不参与计算。

- [ ] **步骤 2：实现普通变更直达路径**

读取每个目标文件一次，使用读到的 SHA 生成 operations，调用一次带稳定 key 的 change_prepare，然后从结果原样复制 changeset_id 和 expected_digest 给 change_apply。在此路径禁止 change_read(history) 与 change_read(diff)。

- [ ] **步骤 3：实现基于 error.details 的恢复分支**

客户端必须按结构化字段执行：

    PATCH_TOO_MANY_FILES：
      splitByDistinctPath(operations, details.max_patch_files)
      每个 batch 使用 logical_change_id:batch:<index> 作为独立 idempotency_key

    STALE_REVISION / PATCH_CONTEXT_* / PATCH_HUNKS_OVERLAP：
      读取 details.failed_path 的完整当前版本和 SHA
      只重建该 path 的 operation
      使用 logical_change_id:retry:<path>:<sha256> 准备该操作

同一 batch 的网络重试复用原 key 和完全相同 payload。相同 path、base_sha256、error_code 连续失败两次后进入 blocked，记录 error.details 作为用户可见证据。

- [ ] **步骤 4：增加客户端回放测试**

用本会话的简化样本覆盖：

    history 已返回 applied -> 直接 prepare，history 调用次数 = 1
    24 个 path 且上限 20 -> 自动分为 20 + 4，不调用 history
    第 7 个 operation stale -> 只读取 failed_path，重试 payload 不包含其余 6 个 path
    同一 path 两次 context/hunk 失败 -> 状态 blocked，第三次不发 MCP 请求

验收统计输出每个 logical_change_id 的工具调用数、读缓存命中数、prepare 次数、局部重试次数和阻塞原因。

### 任务 7：灰度、监控与回归验收

**文件：**

- 修改：internal/server/acceptance_protocol_test.go
- 创建：docs/superpowers/specs/2026-08-06-model-friendly-change-contract.md

- [ ] **步骤 1：记录上线前基线**

以 observation_events 的 remote_session_id 为维度记录：history 次数、无写操作间的重复读取数、每个逻辑变更的 prepare 次数、错误码序列和最终 draft/applied 数。基线报告必须保留本计划开头的 28 次 history 读取和 24/12/12/7/7/7/5 operation 重试链。

- [ ] **步骤 2：灰度启用客户端状态机**

先仅对一个客户端模型版本启用去重和局部恢复，保留原始工具调用审计。观察至少 20 个成功或阻塞的逻辑变更；若任一变更出现相同 read 指纹第二次网络请求，记录 workflow_dedup_miss 并保留 logical_change_id 供回放。

- [ ] **步骤 3：核验量化目标**

验收报告必须同时满足：

    相同 read 指纹的重复网络调用 = 0
    普通单文件变更的中位 MCP 调用数 <= 3
    同一逻辑变更的重复 draft = 0
    可定位 stale/context/hunk 错误中，failed_path 局部恢复占比 = 100%
    终端 history 摘要不含 data=object 或 meta=object

任一项不满足时，按 logical_change_id 回放状态机转移和 ARC data，先修复契约或客户端调度，再扩大灰度。

- [ ] **步骤 4：执行完整回归与格式检查**

运行：

    rtk gofmt -w internal/changeset/service.go internal/changeset/service_test.go internal/server/tools_changeset.go internal/server/tools_change_recovery_test.go internal/server/agent_guidance.go internal/server/agent_guidance_test.go internal/server/tools_catalog.go internal/observation/render.go internal/observation/render_test.go internal/server/acceptance_protocol_test.go
    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./... -count=1
    rtk env GOCACHE=/tmp/mcpx-go-cache go vet ./...

预期：全部通过。仓库规则禁止自动提交，完成后只展示变更摘要、测试结果和建议的中文 Conventional Commit 信息，等待用户明确确认后再执行提交。

### 任务 8：把文件格式变成读取和变更的强契约

**文件：**

- 修改：internal/file/read.go
- 修改：internal/file/file_test.go
- 修改：internal/server/tools_source_unified.go
- 修改：internal/server/tools_source_unified_test.go
- 修改：internal/server/tools_changeset.go
- 修改：internal/server/tools_change_recovery_test.go
- 修改：internal/server/tools_catalog.go
- 修改：internal/server/agent_guidance.go
- 修改：internal/server/agent_guidance_test.go
- 修改：docs/superpowers/specs/2026-08-06-model-friendly-change-contract.md

- [ ] **步骤 1：冻结原始字节格式模型并补齐底层测试**

在 `internal/file` 定义可序列化的 `Format`，字段与客户端契约保持一致。检测顺序为 BOM（UTF-8、UTF-16LE、UTF-16BE）-> 字节有效性 -> 换行计数 -> 尾字节；检测不得依赖已被字符串归一化的内容。为 `Read` 和 `ReadFull` 编写同一张表驱动测试，至少覆盖：UTF-8 LF 且有末尾换行、UTF-8 CRLF 且无末尾换行、UTF-8 BOM、UTF-16LE BOM、混合换行、空文件和二进制文件。

断言窗口读取和完整读取对同一文件给出相同的 `format`，并且 SHA-256 继续针对原始字节。UTF-16/BOM 或二进制无法作为 UTF-8 窗口交给模型时，返回明确的 `charset` 与不可编辑/需完整 base64 语义，不能退化为含糊的 “binary or non-utf8”。

- [ ] **步骤 2：让三种 source_read 结果具有同形 format**

在 `toolFileReadUnified` 的 window、full、batch 分支都嵌入 `format`。full 分支保留既有 `encoding` 兼容字段，但将它明确为传输编码；batch 中每个 `results[]` 项独立携带 `format`。`sourceReadDisplay` 与 `fullFileReadDisplay` 至少展示：字符集/BOM、换行类型、末尾换行状态；混合换行显示计数，避免模型把它误认为统一 CRLF 或 LF。

在 `tools_source_unified_test.go` 中针对 `mode=window`、`mode=full` 和 `items[]` 解码 `StructuredContent`，断言 format 深度相等。保留现有 `line_ending` 顶层字段一个发布周期，标记为兼容别名，客户端只应使用 `format.line_ending`。

- [ ] **步骤 3：将格式保留纳入 Changeset 预检和结果**

在 changeset 准备期间从原始文件及 proposed bytes 计算 `original_format` 和 `proposed_format`。默认策略为 `preserve`：字符集、BOM、换行主类型和末尾换行改变时返回 `FORMAT_CHANGED`，在 `error.details` 中提供 `failed_path`、两份 format 和 `recovery_plan`；不要创建 draft。`mixed` 文件只允许局部编辑保持既有字节序列，若无法验证则要求客户端先以完整模式重读，不允许整文件规范化。

每个成功 prepared 文件在结果中返回 `original_format`、`proposed_format`、`format_preserved=true`。用户明确请求格式化时，使用显式 `format_policy=normalize`，记录用户确认来源和目标 format；该分支不能由模型自行选择。`tools_catalog.go` 和 agent guidance 必须说明：修改文本前先读取 format；没有成功读取的 format 时不能生成 patch。

- [ ] **步骤 4：回放 CRLF 故障并执行验证**

用日志中 `IhPatientDoctorQueryService.java` 的等价 CRLF fixture 构造局部方法插入：首次读取即获得 format，随后 `replace_exact` 或 `insert_before` 仅改动目标行，准备结果 `format_preserved=true`，最终 unified diff 不得显示整文件换行替换。再覆盖 UTF-8 BOM 无内容改动、末尾换行改变、混合换行与 UTF-16 拒绝/显式处理。

运行：

    rtk env GOCACHE=/tmp/mcpx-go-cache go test ./internal/file ./internal/changeset ./internal/server -run 'Test.*(Format|LineEnding|SourceRead|ChangePrepare)' -count=1

预期：通过；客户端可在第一次 `source_read` 后准确决定补丁字节格式，日志中不再出现“CRLF 试验”“换行不确定”或因格式改变放弃草稿的恢复循环。

## 依赖与交付顺序

1. 任务 1 固化契约和基线。
2. 任务 2、任务 3 先让服务端能够精确定位失败 operation。
3. 任务 4、任务 5 让模型在 MCP 响应和终端中看到明确状态，并收到无循环指导。
4. 任务 6 由客户端负责人基于稳定的 ARC 字段实现状态机。
5. 任务 8 让读取与 Changeset 在开始灰度前具备同一份格式基线。
6. 任务 7 使用真实观测数据灰度验证，指标达标后才设为默认。

## 规格覆盖自检

- 28 次 history 重复：由客户端读取指纹、history 禁用作预检、模型指导和灰度指标覆盖。
- 多次草稿：由稳定 idempotency_key、服务端回放测试和客户端批次键覆盖。
- 24/12/7 operation 盲重试：由文件上限结构化恢复和客户端分批策略覆盖。
- stale/hunk/context 漂移：由 OperationError、精确 failed_path 和两次重试上限覆盖。
- 编码、BOM、换行与末尾换行猜测：由统一 format、Changeset 格式保留预检和 CRLF 回放覆盖。
- data=object meta=object：由 history DTO、StructuredContent 和观测专用渲染覆盖。
- 公开契约兼容性：保留原文本 Envelope，同时在 ARC/StructuredContent 增加字段，并通过公开协议测试覆盖。

# MCPX 客户端展示、Artifact 编码与生产契约修复计划

## 目标

基于 2026-08-09 对 demo Workspace 的 86/100 评估结果和一次性 connector 调用诊断，修复 MCPX 在生产接入前仍存在的公开契约、客户端引导、风险标记、响应留存和内容呈现问题，使远端模型与人类用户都能准确理解：

- 模型的目标、操作目的、当前进度、下一步以及 Plan/Task/Operation 关联；
- 工具调用的真实 MCP 结构，而不是被包装成乱码或双重 JSON 的内部路由；
- 注册 Artifact 的原始编码、展示编码和二进制交付方式；
- Plan evidence 的合法类型及其可验证引用；
- Observe 中 Plan Task 与执行 Task 的明确边界；
- 中文 Context 检索的排序依据和作用域；
- 当前限制、构建版本、Schema revision 和实际工具风险。

本计划对应一次公开契约重构。完成后发布新版本，不保留旧字段、旧别名或兼容 handler。

## 架构与技术约束

- 语言与运行时：Go 1.26.1+，标准库 testing。
- 工具协议：MCP tools/list、tools/call、structuredContent、outputSchema、_meta。
- 展示链路：MCPX 负责返回结构化结果、可读摘要和客户端引导；外部客户端不得把原始调用 JSON 当作主展示内容。
- 文件内容：原始字节 SHA-256 保持不变；文本统一交付 UTF-8；二进制统一走 Blob 或明确的 base64 数据通道。
- 安全边界：注册 Workspace root、SHA revision、幂等键、审计事件和现有路径隔离规则继续作为强约束。
- 版本策略：本轮公开 schema 有意修改，实施完成后将版本从当前 0.4.0 提升到 0.5.0，并生成新的 tool_schema_revision。
- 测试策略：以当前 schema 和当前 handler 为唯一契约；优先保留公开协议、关键边界和端到端回归，不为实现细节堆叠重复单元测试。
- 日志策略：原始 connector 调用导出只作为一次性诊断，不进入仓库，不维护 replay fixture。

## 一次性 connector 诊断结论

### 已确认的客户端导出缺口

文件包含 22 次 mcpx 调用：

- 21 次 toolOutput 为 null，仅 1 次 artifact.register 保留了附件元数据；
- 22 次 toolResponseMetadata 全部为空；
- widget.template、widget.state、widget.responseMetadata、widget.externalCallTimeMs 全部为空；
- 22 次调用都携带 goal 和 purpose；
- 21 次携带 reasoning_summary，18 次携带 progress_summary，14 次携带 next_step；
- 输入 JSON 平均约 600 字节，最大约 726 字节，且上下文字段的完整性不一致。

这说明导出层无法单独证明 MCPX 是否返回了结果，也无法为人类展示提供可靠的响应状态、延迟、事件 ID 或结构化结果。需要增加真实 MCP wire capture 和 connector round-trip 验收，不能只根据模型的自然语言总结判断调用成功。

### 已确认的风险标记错误

日志中的 badges 显示：

- plan.create、plan.advance、plan.complete 被标记为 write + open_world + destructive；
- artifact.register 被标记为 write + open_world + destructive；
- artifact.read 被标记为 write + open_world + destructive；
- execute.run 被标记为 write + open_world；
- edit 被统一标记为 write + destructive。

其中 artifact.read 不应是写操作或破坏性操作；plan 的持久化状态变化不等于 open-world 或破坏性文件操作；artifact.register 只是登记已受 Workspace policy 允许的产物，不应被归类为破坏性外部操作。混合 action 工具使用最高风险默认值，会造成安全层误拦，也会掩盖真正需要确认的 execute/remove 风险。

### 已确认的敏感信息泄露风险

artifact.register 的附件元数据中包含带签名查询参数的临时 download_url。即使 URL 有效期有限，也必须按凭据处理：

- 不把完整签名 URL 写入 durable history、ARC 正文、评估报告或仓库 fixture；
- 导出日志只保留 attachment_id、MIME、大小和脱敏后的资源标识；
- 需要下载时由客户端通过一次性资源引用获取，不把可重放 URL 传播到模型上下文。

## 已确认问题与处理决策

| 问题 | 处理决策 |
| --- | --- |
| 远端客户端展示 {"text":"{...args...}"}、/mcpx/link_* 或截断 JSON | MCPX 主文本只返回可读摘要；完整机器结果放入 structuredContent 和 _meta[mcpx.result]；原始调用 JSON 仅允许进入 debug 视图。 |
| ARC 中目标、目的、进度、下一步、Plan/Task/Operation 逐行重复 | 统一成紧凑上下文块，按语义组合展示；不同工具和状态使用稳定颜色，连续操作按 operation 分隔。 |
| 22 次导出调用大多没有结果 | 规定 connector 必须保留 MCP content、structuredContent、_meta 和 response metadata；服务端增加可验证的响应 envelope 和 wire fixture。 |
| Artifact 注册/读取出现乱码 | 注册返回元数据与资源链接，读取按源编码解码；UTF-8/UTF-16 文本以 UTF-8 交付，未知/二进制以 Blob/base64 交付。 |
| 工具 risk badge 过度或错误 | 按 action 拆分风险语义；read/list 标为只读，plan 标为非破坏性状态写入，artifact.read 标为只读，execute/remove 显式标为受限破坏性能力。 |
| plan.complete schema 声明 read，服务端拒绝 | 建立单一 evidence 枚举；read 和 observe 成为正式可验证类型，其余未列入当前契约的类型移除。 |
| observe.task_id 同时表示 Plan Task 与执行 Task | Observe 输入拆为 plan_task_id 与 execution_task_id；执行状态、日志、attach 只接受 execution_task_id。 |
| 中文 smart context 首段排序偏词法 | 实现确定性的短语、中文词项、标识符和位置加权排序，并返回 score 与命中原因。 |
| 注册附件可能泄露临时签名 URL | 对 response、audit、export 和 fixture 做统一脱敏；真实资源通过短期内部引用交付。 |
| 每次调用重复发送多个 ARC 字段 | 使用单一 context 对象表达上下文，按状态变化发送；移除平铺重复字段和不完整的半套上下文。 |

## 实施步骤

### 1. 固化 MCP 响应与客户端展示契约

修改范围：

- internal/arc/arc.go
- internal/arc/human.go
- internal/server/observability.go
- internal/server/observation_bridge.go
- internal/server/agent_guidance.go
- internal/server/guidance/agent.yaml
- internal/server/prompts/tools.yaml
- internal/mcpproxy/call.go
- internal/mcpproxy/proxy.go

实现内容：

1. 固化 MCP 原生调用边界：远端调用必须是 tools/call(name, arguments)，服务端不得生成或要求 /mcpx/link_* 作为工具协议；internal/mcpproxy 继续使用 mcp.CallToolParams，增加 wire-level 断言确保 name 和 arguments 不被双重 JSON 化。
2. 重新定义结果分层：
   - content[0].text：人类可读的短摘要，不输出完整 args、裸 JSON、内部 link 或无意义的 Progress model summary 标题；
   - structuredContent：模型消费的完整、稳定字段；
   - _meta[mcpx.result]：完整 ARC envelope、事件 ID、请求 ID、operation 关联和恢复字段；
   - 调试原始请求：仅通过显式 debug 字段或 debug 视图暴露。
3. 统一响应 metadata，至少包含 request_id、event_id、tool、status、server_elapsed_ms、next_action 和可用的 operation_id；不把 trace 中的密钥、签名 URL 或原始敏感参数带入模型上下文。
4. ARC 统一展示 goal、purpose、progress、next、plan、task、operation：
   - 目标与目的放在同一上下文行；
   - 进度与下一步放在同一上下文行；
   - Plan/Task/Operation 作为关联尾部组合展示；
   - tool/status 作为主标题；
   - 连续 operation 之间输出明确分隔线；
   - tool、成功/等待/失败/阻塞等状态使用固定语义颜色；
   - 长字段有可读截断标记，机器字段保持完整。
5. 建立单一 context 对象，字段为 goal、purpose、reasoning_summary、progress、next、plan_id、plan_task_id、execution_task_id、operation_id。上下文只在首次调用或状态发生变化时发送；工具专属参数不再重复承载同一批 ARC 字段。
6. 为远端客户端补充明确引导：模型读取 structuredContent 生成自然语言进度，不把调用参数原样复述给用户；破坏性操作、等待确认、失败恢复和下一步都必须由服务端结构化提供。
7. 增加 outputSchema 的展示字段约束，使客户端可以在不解析人类文本的情况下理解 summary、context、result、next_action、display 和关联 ID。

验收测试：

- plan.complete、artifact.register、edit、execute、observe 各生成一条真实结果，主文本不包含 {"text":、"args"、完整原始参数 JSON 或 /mcpx/link_。
- structuredContent 中保留完整目标、目的、进度、下一步及关联 ID。
- 长 reasoning_summary、中文内容、换行、引号和 Unicode 不会破坏 JSON，也不会被错误地当作注册文件内容。
- MCP proxy wire fixture 断言真实请求为 name=plan、arguments.action=complete，而不是把 JSON 放入 text 字符串。
- connector round-trip fixture 保留 content、structuredContent、_meta 和 response metadata；工具输出不再被统一导出为 null。
- ARC golden output 覆盖 started、succeeded、waiting_confirmation、failed、blocked 五种状态，并验证 operation 分隔和颜色标记。
- 调用上下文在重复读取场景下不再强制重复 goal/purpose/reasoning/progress 全量字段。

测试文件：

- internal/arc/presentation_test.go
- internal/arc/human_test.go
- internal/server/observability_test.go
- internal/server/acceptance_protocol_test.go
- internal/server/agent_guidance_test.go
- internal/mcpproxy/proxy_test.go

如果外部远端客户端不在本仓库，以上 wire fixture 作为 MCPX 侧边界证明；客户端 UI 适配需按同一字段契约单独验证，不将第三方 UI 行为伪装成服务端已修复。

### 2. 按 action 修正工具风险标记与公开目录

修改范围：

- internal/server/tools_catalog.go
- internal/server/tools_clean_core.go
- internal/server/tools_artifact.go
- internal/server/tools_artifact_clean.go
- internal/server/tools_plan.go
- internal/server/tools_plan_clean.go
- internal/server/tools_execute.go
- internal/server/public_catalog_test.go
- internal/server/acceptance_protocol_test.go
- internal/server/agent_guidance.go
- README.md

实现内容：

1. 对现有工具目录逐项建立 risk matrix，禁止用一个混合 action 工具的最高风险值覆盖所有 action。
2. read、observe、runtime_read、environment_read、artifact list/read 统一声明只读、非破坏性、非 open-world。
3. plan create/read/advance/complete/block/replan/deliver 声明为服务端状态写入，但非破坏性、非 open-world；plan 不触发文件删除或任意外部世界操作。
4. artifact register 声明为受 Workspace policy 限制的产物登记写入，非破坏性、非 open-world；artifact read/list 纯只读。
5. edit 的 create/update/rename 和 remove 的 prepare/submit 分开声明；真正的 remove submit 才声明 destructiveHint=true、openWorldHint=false、idempotentHint=true，并在 description 中写明 registered-workspace-only、manifest/revision guarded、no shell、regular/symlink-safe semantics。
6. execute 保持独立的命令执行风险声明，明确 workspace scope、shell policy、命令可能改变文件或启动外部进程；不能用 plan/artifact 的风险标记替代 execute 的真实风险。
7. 对需要宿主确认的工具，公开固定的结构化字段和安全说明，不依赖 purpose 或自然语言中“安全”一词诱导安全层。
8. 由于不保留旧版本兼容，移除旧的混合风险假设、旧字段和旧 action 别名；更新 tools/list 期望集合和 schema revision。

验收测试：

- 工具目录中的 badge/annotations 与 action 语义逐项一致。
- artifact.read 不再出现 write、destructive 或 open_world 标记。
- plan 生命周期不再出现 destructive 或 open_world 标记。
- remove submit 被识别为受限 Workspace 文件/目录移出，而不是任意 shell destructive command。
- tools/list、README、ARC metadata 和 connector badge fixture 对同一风险矩阵给出相同结论。
- 真实 Host/connector approval fixture 能区分 read、metadata write、workspace edit、remove submit、execute。

### 3. 修复 Artifact 注册文件乱码与编码窗口契约

修改范围：

- internal/artifact/encoding.go
- internal/artifact/service.go
- internal/artifact/encoding_test.go
- internal/artifact/service_test.go
- internal/server/tools_artifact.go
- internal/server/tools_artifact_clean.go
- internal/server/tools_development_test.go
- internal/server/acceptance_protocol_test.go

实现内容：

1. 区分“注册确认”和“读取内容”：artifact.register 只返回 artifact_id、路径、大小、原始 SHA、MIME、源编码探测结果和资源链接，不把原始文件字节塞进主文本。
2. 在 Artifact 元数据/读取结果中明确返回：
   - source_encoding：utf-8、utf-16le、utf-16be、binary、unknown；
   - source_bom；
   - delivery_encoding：utf-8、base64、blob；
   - mime_type；
   - source_offset、next_source_offset、eof；
   - 原始字节 SHA-256。
3. 文本文件统一解码为 UTF-8；UTF-8 BOM 不进入展示内容；UTF-16 必须在文件头探测编码后再进行窗口读取，不能因为窗口从奇数字节开始或不含 BOM 而按 UTF-8 解释。
4. UTF-16 窗口按 code unit 边界对齐，不能返回半个字符；续读 offset 使用源字节坐标并返回服务端实际对齐后的下一个 offset。
5. 二进制和无法可靠解码的文件不进入 content[0].text，资源读取返回 Blob，工具结果返回明确的 base64/Blob 交付标记，客户端引导显示“二进制内容”而不是乱码。
6. 每次读取仍以原始字节计算/校验 SHA；格式转换只影响交付，不改变源文件身份。
7. 资源链接返回的 MIME 必须带正确 charset；下载附件只传递原始字节和准确 MIME，不在 connector 层重复按错误 charset 转码。
8. outputSchema 固定注册和读取的编码枚举，客户端据此选择纯文本、资源预览或下载路径。

验收测试：

- UTF-8 中文、UTF-8 BOM、UTF-16LE BOM、UTF-16BE BOM、混合换行分别通过 artifact.register → artifact.read，展示无乱码且返回正确 encoding。
- UTF-16 从中间窗口开始读取、连续读取和 EOF 读取均不出现半字符。
- 非文本二进制不会出现在人类摘要中，不会被强行按 UTF-8 渲染。
- 注册后的 resource 读取与 artifact(action=read) 返回相同的解码语义。
- 乱码输入、截断输入、未知 MIME 不崩溃，不返回裸 interface 或不可解析的错误。
- MCPX 自身响应、history 与 ARC 不写入 token、secret 等敏感值；原始 connector 导出不纳入仓库交付。

### 4. 统一 Plan evidence schema 与服务端验证

修改范围：

- internal/plan/types.go
- internal/plan/service.go
- internal/plan/service_test.go
- internal/server/tools_plan.go
- internal/server/tools_plan_clean.go
- internal/server/clean_core_p1_p3_test.go
- internal/server/acceptance_protocol_test.go
- internal/arc/arc.go

当前公开枚举：

read | edit | execute | artifact | source | verification | observe

changeset、execution_task、task、test、validation 不再作为公开输入或兼容别名；它们分别归入 edit、execute 或 verification 的明确引用语义。

实现内容：

1. 在 internal/plan/types.go 建立唯一的 evidence kind 定义、描述和校验函数；schema、解码器、服务层和 guidance 全部引用同一份定义。
2. read 引用必须指向当前 remote_session_id 下已完成的 read 操作/事件；不能接受任意文本冒充读取证据。
3. observe 引用必须指向当前会话的持久化观测事件或已完成 operation；验证工具类型、会话归属和完成状态。
4. edit、execute、artifact、source、verification 分别验证对应持久化记录、执行结果、Artifact、受限路径或验证事件。
5. evidence 为空、引用不存在、跨 session、状态未完成、kind 不匹配时返回统一 PLAN_INVALID_REQUEST，并给出可执行的 recovery。
6. 公开 outputSchema 使用精确 enum，禁止 schema 声明一个服务端不会接受的类型。
7. 完成结果中的 evidence 以结构化数组回显 kind、reference_id、validated、source_event_id，避免模型只能依赖自然语言确认。

验收测试：

- 对上述七种 kind 各跑一次 create → advance → complete → deliver，全部使用真实引用。
- read 和 observe 的非法引用、跨 session 引用、未完成 operation 引用均被拒绝且无状态变化。
- schema enum、错误信息、recovery guidance 与 handler 行为完全一致。
- 不再接受已移除的旧 kind，防止出现“表面兼容、运行时含义不同”。

### 5. 拆分 Observe 的 Plan Task 与执行 Task 语义

修改范围：

- internal/server/tools_observe.go
- internal/server/tools_execute.go
- internal/server/tools_clean_core.go
- internal/server/tools_public_adapters.go
- internal/arc/arc.go
- internal/server/observation_bridge.go
- internal/observation/event
- internal/observation/render
- internal/server/tools_observe_test.go
- internal/server/acceptance_protocol_test.go

新契约：

- plan_task_id：只表示 Plan 中的规划任务，供 Plan 状态、历史筛选和关联展示使用。
- execution_task_id：只表示 execute 脱离出来的终端执行任务，供 status、logs、attach 和停止操作使用。
- Observe 不再接受含义不明确的顶层 task_id。
- ARC 上下文同时暴露 plan_task_id 与 execution_task_id，不再使用单一 task_id 覆盖两种实体。

实现内容：

1. 重写 observe schema、路由和参数错误，使 status/logs 明确要求 execution_task_id，Plan 查询明确要求 plan_task_id。
2. 历史、changes、diff、logs 的结果中分别输出关联字段，避免客户端根据字符串猜测 ID 类型。
3. 执行 Task 不存在、把 Plan Task ID 传给执行视图、跨 session 查询时返回专门的错误代码和 recovery。
4. 更新 ARC、guidance、README 和工具描述中的示例，禁止继续展示裸 task_id。
5. 将 plan_task_id、execution_task_id 贯穿 observation event、audit record、operation step 和 connector export。

验收测试：

- execute → execution_task_id → observe(status/logs/attach) 完整通过。
- plan → plan_task_id → observe(plan view/history) 完整通过。
- 将 Plan Task ID 传入执行日志视图不会误查或返回含义模糊的 NOT_FOUND。
- 跨 session、空 ID、两种 ID 同时传入均得到结构化校验错误。
- 导出日志中能根据显式字段重建 Plan Task 与执行 Task 关联。

### 6. 提升中文 Context 检索的确定性与可解释性

修改范围：

- internal/source/query_analyzer.go
- internal/source/smart_query.go
- internal/source/source.go
- internal/source/smart_query_regression_test.go
- internal/source/query_priority_regression_test.go
- internal/source/query_scope_regression_test.go
- internal/server/tools_context_test.go
- internal/server/tools_source_unified.go

实现内容：

1. 在不引入外部 embedding 服务的前提下实现确定性排序：完整短语命中 > 中文词项/二元组覆盖 > 英文标识符精确命中 > 标题/路径命中 > 普通词法命中。
2. 对命中位置、连续性、字段权重和重复命中进行稳定加权，优先返回真正包含目标语句的窗口，而不是文件开头的泛化内容。
3. 结果增加 score、rank、matched_terms、match_reason，明确标记 lexical 或 heuristic；不把词法排序描述成语义 embedding。
4. paths 继续作为 hard scope；排序不能突破作用域；分页和窗口续读必须保持排序稳定。
5. guidance 告知模型：需要高置信语义检索时先用 exact/search 缩小范围，再使用 context 获取上下文。

验收测试：

- 复现报告中的“键盘控制暂停播放、重置和速度”中文查询，目标段落排在首位或首个相关窗口。
- 中文、英文标识符和混合查询均有稳定 score 与命中原因。
- scope、分页、重复查询和无命中结果不回归。

### 7. 发布限制、构建 provenance 与批处理观测

修改范围：

- internal/server/limits.go
- internal/server/capabilities.go
- internal/server/tools_runtime.go
- internal/server/tools_operation.go
- internal/server/operation_runtime.go
- internal/server/observability.go
- internal/server/public_catalog_test.go
- internal/server/tools_operation_test.go
- README.md
- docs/plans/2026-08-08-clean-core-p0-implementation.md
- docs/plans/2026-08-08-clean-core-p1-p4-implementation.md

实现内容：

1. 在机器可读 capability metadata 中稳定发布：
   - read.max_source_bytes=4194304；
   - read.max_items=20；
   - operation_batch.max_steps=32；
   - edit.max_changed_lines=1000。
2. runtime metadata 发布 version、build_commit、build_time、tool_schema_revision 和关键能力集合；缺省值也必须结构化，不返回乱码或空白日期。
3. operation batch 结果增加轻量统计：调度等待、服务端耗时、最大并发度、步骤成功/失败数和可选 p50/p95；不把全部事件复制进结果正文。
4. 更新 README 的工具调用示例、ARC 展示说明、Artifact 编码说明、Plan evidence、Observe ID 和 risk annotation 说明。
5. 修订现有实现计划中的过期工具名、版本号和旧字段示例，保留历史决策但不继续把旧接口作为可用契约。
6. 移出原始 connector 诊断导出，不在仓库维护调用日志 fixture。

验收测试：

- tools/list、runtime/capabilities、outputSchema 三处限制值一致。
- build provenance 与 tool_schema_revision 可通过一次读取获得，且和实际服务实例一致。
- 32-step batch 的统计字段存在、数值一致、响应大小受控。
- 仓库中不存在原始 connector 调用导出或签名下载 URL fixture。

### 8. 端到端验收与报告更新

验收矩阵：

1. 通过 Streamable HTTP 执行真实 tools/list → tools/call，验证 MCP 原生调用结构。
2. 验证 ARC 人类展示：工具、状态、上下文组合、operation 分隔、颜色和 diff 摘要。
3. 验证 Artifact：注册、资源读取、UTF-8/UTF-16、二进制和连续窗口。
4. 验证 Plan 七种 evidence kind 的完整状态机。
5. 验证 Observe 的两类 Task ID、跨客户端接力和错误恢复。
6. 验证中文 Context、hard scope、分页和 score 解释。
7. 验证风险标记能将 read、metadata write、workspace edit、remove submit、execute 区分开。
8. 验证限制、schema revision、build provenance 和 32-step operation 统计。
9. 验证服务重启后历史、Artifact metadata、Plan evidence 和 execution Task 查询语义。
10. 验证 connector/export 能保存响应，不再出现 21/22 toolOutput 缺失；输出脱敏后仍能关联 request、event、operation 和结果。

验证命令：

- go test ./internal/arc ./internal/artifact ./internal/plan ./internal/source ./internal/mcpproxy ./internal/server -count=1
- go test ./... -count=1
- go test -race ./... -count=1
- go vet ./...
- test -z "$(gofmt -l ./cmd ./internal)"
- CGO_ENABLED=0 go build -o /tmp/mcpx-server-check ./cmd/mcpx-server
- git diff --check

报告交付：

- 新增 docs/evaluations/2026-08-09-client-presentation-artifact-contract.md，记录实际版本、schema revision、Remote Session、测试矩阵、失败证据和未覆盖项。
- 报告区分“服务端已通过”“客户端边界已验证”“第三方 UI 不在本仓库控制范围”三种结论。
- 乱码问题必须同时提供原始字节 SHA、source_encoding、delivery_encoding 和实际展示样本，不能只写“已修复”。
- 只在所有专项验收通过后把 86/100 的 Conditional Go 结论更新为新的评分，不以单元测试通过替代真实 MCP 调用验证。

## 实施顺序与交付检查点

### 阶段一：展示、响应留存与编码

- 完成第 1、2、3 项，先解决远端模型无法读懂调用结果、工具风险误标和注册文件乱码。
- 交付：ARC golden output、MCP wire fixture、connector round-trip fixture、Artifact 编码回归、脱敏导出 fixture。

### 阶段二：公开契约

- 完成第 4、5 项，统一 Plan evidence 和 Observe Task ID。
- 交付：schema/handler/service 三层一致性测试、当前版本 schema revision。

### 阶段三：检索与运行时信息

- 完成第 6、7 项，补足中文检索解释和生产 metadata。
- 交付：scope/排序回归、限制与 provenance 验收、批处理统计。

### 阶段四：端到端复测

- 完成第 8 项并更新评估报告。
- 交付：当前版本评估报告和明确的剩余风险清单。

每个阶段只在相关测试和 git diff --check 通过后进入下一阶段。代码提交需要用户在对应阶段完成后单独授权；本计划创建阶段不提交实现代码。

## 完成定义

- 人类看到的是可读的工具状态卡和 ARC 上下文，而不是原始调用 JSON。
- 模型可从 structuredContent 精确获得目标、目的、进度、下一步以及 Plan/Task/Operation 关联。
- connector/export 能保留真实 MCP 结果、响应 metadata 和可审计关联，不再把大多数调用记录成 null。
- Artifact 文本不乱码，二进制不伪装成文本，窗口续读不破坏 UTF-16 code unit。
- Artifact 和工具日志不泄露签名下载 URL、secret 或可重放凭据。
- Plan evidence schema 与服务端支持集合完全一致，read/observe 引用可验证。
- Observe 不再混淆 Plan Task 和执行 Task。
- 工具 risk annotation 与真实副作用一致，read 不触发破坏性审批，remove/execute 的风险可机器判定。
- 中文 Context 排序可解释、作用域严格、分页稳定。
- 限制、构建 provenance、schema revision 和 operation 统计可机器读取。
- 端到端报告能够清楚区分 MCPX 行为与外部客户端 UI 行为。


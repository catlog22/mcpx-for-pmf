# Clean Core P1–P4 Evaluation

## 目的与范围

这份评估使用仓库内临时 Workspace、fake Skill 和 fake stdio MCP server，不访问公网、不读取真实凭证，也不依赖宿主 UI。可执行场景位于 `internal/server` 的 `clean_core_*`、P0 edit 测试和 Streamable HTTP acceptance；它们共同构成计划要求的等价 evaluation test package。

运行命令：

```bash
go test ./internal/edit ./internal/idempotency ./internal/server ./internal/plan -count=1 -v
go test ./... -count=1
```

## 场景矩阵

| 场景 | 可复现证据 | 工具轮次门槛 |
| --- | --- | --- |
| session → read → edit → execute → observe | `TestA01A02A03A07A10A13ViaMCPProtocol`、`TestCleanCorePlanEvidenceAndArtifactWorkflow` | 正常开发路径按阶段调用，不依赖文本解析 |
| STALE / MATCH / POLICY / TOO_MANY_CHANGES / 确认恢复 | `TestStaleRevision`、`TestMatchNotFoundAndAmbiguous`、`TestCleanCoreExecuteIdempotencyAndUserConfirmation`、`TestTooManyChangesBoundary` | 每个错误返回结构化 code 与 recovery |
| 跨客户端 session 接力 | `TestCleanCoreSessionAttachReusesOnlySuppliedID`、`TestA01A02A03A07A10A13ViaMCPProtocol` | attach 只复用已返回的 `remote_session_id` |
| 多文件 edit、长任务、offset/cursor 续读 | `TestBatchTwoFilesLineCap`、`TestCleanCoreEditBoundsLargeDiffAndPaginatesFullDiff`、HTTP acceptance | 继续调用只携带服务端返回的 ID、offset 或 cursor |
| plan evidence → complete → deliver | `TestCleanCorePlanEvidenceAndArtifactWorkflow` | edit、execute、artifact 三类 evidence 齐全后才 ready |
| artifact 文本/二进制与分片读取 | `internal/artifact` 测试与 `TestCleanCorePlanEvidenceAndArtifactWorkflow` | `offset`/`limit` 有界，文本保持 UTF-8 边界 |
| discover → skill_call / discover → mcp_call | `TestCleanCoreDiscoveryIsAnExplicitExtraCall`、`TestCleanCoreMCPDiscoveryRequiresExtraCall` | 合规链路恰好 2 次；跳过发现为错误 → discover → call，3 次 |
| catalog / bootstrap / capability / guidance 一致性 | `TestCapabilityCatalogMatchesRegisteredTools`、HTTP acceptance | 最终 core/support 17 工具集合无旧公开名 |
| 大 diff 预览与分页、Unicode 边界 | `TestCleanCoreEditBoundsLargeDiffAndPaginatesFullDiff`、`internal/server/diff_preview.go` 相关测试 | 默认单文件 ≤32 KiB、总预览 ≤64 KiB |
| UTF-16LE/BE read → edit → SHA/格式 round-trip | `TestCleanCoreUTF16ReadEditRoundTripPreservesRawBytes`、`TestPreserveUTF16LEBOM`、`TestPreserveUTF16MixedLineEndings` | 模型看 Unicode，原始编码、BOM、换行和原始 SHA 保留 |
| 幂等 replay / conflict / 并发 / restart 状态 | `TestStoreReplayConflictAndPersistence`、`TestStoreMergesInFlightRequests`、`TestCleanCoreEditRejectsIdempotencyFingerprintConflict` | 相同指纹不重复写；冲突和 in_doubt 在一次错误响应中给恢复动作 |
| 未 discover 的 Skill/MCP | 上述两个 discovery 测试 | `DISCOVERY_REQUIRED` 明确返回 `required_call_count=1`，且 lease 数量保持为 0 |

## 交互效率检查

- `session → read → edit` 的默认路径不追加完整 diff 调用；需要审阅时才调用 `observe(view=diff)`。
- 长命令启动返回 `task_id` 后，状态和日志分别通过 `observe(view=status|logs)` 读取，等待继续使用 `execute(action=attach)`。
- Skill/MCP 的显式 `discover` 不由服务端隐藏；缺失发现时响应带 `next_action` 和一次额外调用的明确成本。
- 同一幂等 key 的重试只返回 durable replay；参数变化返回 `IDEMPOTENCY_CONFLICT`，不返回原始业务参数或秘密。

## P4 交付门槛

最终收口命令还包括：

```bash
go test -race ./... -count=1
go vet ./...
CGO_ENABLED=0 go build -o bin/mcpx-server ./cmd/mcpx-server
test -z "$(gofmt -l ./cmd ./internal)"
git diff --check
```

运行结果不写入仓库；只以命令退出码、测试名称和失败 code 作为评估证据。

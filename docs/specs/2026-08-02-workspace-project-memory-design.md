# Workspace 项目记忆设计规格

## 目标

为 `workspace_state` 增加 `memory` 操作，让模型按关键词、ID、时间和最新数量检索 Workspace 的项目记忆。记忆以已有观测数据为来源，不新增独立写入链路，不依赖 Git。

## 数据来源

默认只纳入三类事实：

- `progress_report`：主要来源，读取 `summary`、`result_summary`、`status`、`next_step` 和 `related_tool`。
- `file.changed`：读取文件路径、操作类型、目标路径和增删统计。
- `session.lifecycle`：读取会话生命周期摘要。

查询按 Workspace 隔离，使用观测事件的单调 `sequence` 作为记忆 `id`。不纳入普通工具调用、命令输出和原始协议载荷，避免把过程噪声误当成项目事实。

## 请求参数

```json
{
  "action": "memory",
  "keyword": "删除",
  "id": "520~540,548",
  "time": "2026-08-01~2026-08-02",
  "latest": 10
}
```

- `keyword`：对记忆的关键文本做大小写不敏感模糊匹配，匹配 `summary`、结果、下一步、关联工具、路径和操作类型。
- `id`：支持单值（`"526"`）、闭区间（`"520~540"`）、集合（`"526,548"`）和混合表达式（`"520~522,548"`）。服务端解析后去重。
- `time`：支持日期、日期闭区间、日期集合和混合表达式；日期按 Workspace 本地时区解释，结束日期包含全天。
- `latest`：返回匹配结果中最新的条数；默认 10，最大 50。

多个筛选条件使用 AND 关系；结果按 `id` 倒序返回。无结果返回空数组，不视为错误。

## 响应与压缩规则

返回确定性字段投影，不进行 LLM 总结或改写，保证事实文本不失真：

```json
{
  "items": [
    {
      "id": 526,
      "time": "2026-08-02T21:10:00+08:00",
      "type": "progress",
      "status": "completed",
      "summary": "已删除测试缓存",
      "result": "文件清单已收敛",
      "next": "确认最终 Git 差异"
    }
  ],
  "total": 1,
  "has_more": false
}
```

响应只保留 `id`、时间、类型、状态、关键摘要、下一步、文件变更事实等字段；不返回 `input`、`output`、计时字段、`remote_session_id` 或 Resource URI。原文为空的可选字段省略。若底层事件已标记截断，返回 `truncated: true`，不伪造缺失内容。

## 错误与兼容

非法范围、日期或 `latest` 返回明确的 `bad_request`。未知 `action` 沿用现有 `INVALID_ACTION`。非 Git Workspace 仍可查询记忆。该能力作为 `workspace_state` 新 action 发布，不改变现有 action 的响应。

## 数据保留与自动清理

项目记忆查询本身保持只读；数据库清理由 Runtime 的后台 housekeeping 负责，不允许模型传入 SQL、任意表名或任意删除条件。清理策略配置在全局 `~/.mcpx/config.yaml` 的 `state.retention` 下，项目级 `.mcpx.yaml` 不得覆盖：

```yaml
state:
  retention:
    enabled: true
    interval: 24h
    process_event_ttl: 720h       # 30 天
    process_event_max_rows: 10000
    memory_event_ttl: 4320h       # 180 天
    memory_event_max_rows: 2000
    terminal_task_ttl: 720h       # 30 天
    snapshot_ttl: 2160h            # 90 天
    vacuum_threshold_rows: 10000
```

- `process_event` 包含 `tool.started`、普通 `tool.completed`、`command.output` 和 `observer.notice`；满足 TTL 或超过 Workspace 条数上限时，按最旧优先分批删除。
- `memory_event` 包含 `progress_report`、`file.changed` 和 `session.lifecycle`；同样按 TTL 或条数上限清理，默认比过程数据保留更久。
- 仍处于 `active`、`idle`、`blocked` 状态的 Remote Session，其关联会话数据不自动删除；未完成 Changeset、未完成 Plan、未过期 Approval、有效 Idempotency 记录，以及 Remote Session 当前引用的环境快照必须保留。
- 已关闭或归档会话的旧终端任务、日志文件、文件快照和环境快照可按对应 TTL 清理；删除前检查外键引用，失败时跳过并记录原因，不进行级联误删。
- 每轮只做有界批量删除，完成后执行 WAL checkpoint；只有本轮实际删除行数达到 `vacuum_threshold_rows` 且没有活跃写入时才执行 `VACUUM`，避免常驻服务频繁锁库。
- `enabled: false` 完全停止自动清理；配置非法、TTL 为负数或阈值为 0 时启动失败并给出字段级错误。清理失败不影响正常工具调用，但必须写入日志和 `observer.notice`。

默认策略只回收可重建的过程数据，不直接删除 Remote Session、Changeset、Plan 或项目记忆来源。非 Git Workspace 也执行同样的数据库清理，不依赖 Git 状态。

## 测试范围

覆盖单值、范围、集合、混合范围、关键词、日期边界、`latest` 截断、Workspace 隔离、字段投影和无结果查询；验证响应不包含原始协议字段及 Resource URI。

清理测试覆盖默认配置、全局配置不被项目配置覆盖、TTL 与条数上限、按 Workspace 隔离、活跃会话及引用数据保护、分批删除、WAL checkpoint/VACUUM 触发条件、非 Git Workspace 和清理失败不影响正常请求。

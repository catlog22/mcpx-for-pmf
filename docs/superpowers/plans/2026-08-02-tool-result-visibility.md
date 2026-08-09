# 工具结果可见性修复计划

> **执行说明：** 按任务顺序在当前 `dev` 工作区实施；不调整命令解析或安全策略，不提交代码。

## 目标

让模型与终端观测都能直接识别计划 ID、环境快照和关键工具链信息；明确 `workspace_state` 的有效动作，避免模型使用不支持的 `status` 或以复杂 Shell 组合重复探测环境。

## 任务

1. **ARC 计划结果渲染**
   - 修改：`internal/arc/human.go`
   - 为 `plan_manage` 提供稳定的人类可读摘要，保留 `plan_id`、状态、目标与任务数。

2. **终端观测与模型引导**
   - 修改：`internal/observation/render.go`、`internal/server/agent_guidance.go`
   - 为 `plan_manage` 和 `environment_inspect` 的远端信封结果生成精简摘要。
   - 明确 `workspace_state` 的有效动作与环境检查优先级，不放宽 Shell 策略。

3. **回归测试与验证**
   - 修改：对应 ARC、终端渲染和引导测试。
   - 运行 gofmt、针对性测试、全量测试、race 与 vet。

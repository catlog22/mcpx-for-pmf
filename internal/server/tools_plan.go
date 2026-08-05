package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/plan"
	"mcpx/internal/remotesession"
)

func planTaskInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id":     stringSchema("可选的局部依赖引用（本计划内唯一即可）；服务端会生成最终 task_id，后续操作必须使用 plan_create 返回的精确值"),
			"title":       stringSchema("任务标题"),
			"description": stringSchema("任务描述"),
			"depends_on":  arraySchema(map[string]any{"type": "string"}, "必须先完成的任务 ID"),
		},
		"required": []string{"title"},
	}
}

func planEvidenceSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":         stringSchema("证据类型：changeset、execution_task、artifact、source 或 verification"),
			"reference_id": stringSchema("服务端签发的证据引用"),
			"metadata":     map[string]any{"type": "object", "additionalProperties": true},
		},
		"required": []string{"kind", "reference_id"},
	}
}

func planOperationSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":      map[string]any{"type": "string", "enum": []string{"add", "update", "remove"}},
			"task_id":     stringSchema("由 plan_manage create/get 返回的精确 Plan Task ID；不得猜测"),
			"title":       stringSchema("任务标题"),
			"description": stringSchema("任务描述"),
			"depends_on":  arraySchema(map[string]any{"type": "string"}, "必须先完成的任务 ID"),
		},
		"required": []string{"action"},
	}
}

func (r *Runtime) toolPlanManage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action := toolAction(req)
	edit := action != "get"
	envReq, principal, session, fail := r.changeRequest(ctx, req, edit)
	if fail != nil {
		return fail, nil
	}
	planID, _ := envReq.Payload["plan_id"].(string)
	switch action {
	case "create":
		input, err := decodePlanCreate(envReq.Payload)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		created, err := r.plans.Create(ctx, session.ID, principal.ID, input)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, planMap(created))
	case "get":
		item, err := r.plans.Get(ctx, session.ID, planID)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, planMap(item))
	case "start_task":
		taskID, err := requiredPlanTaskID(envReq.Payload)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		task, err := r.plans.StartTask(ctx, session.ID, planID, taskID, principal.ID)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, planTaskMap(planID, task))
	case "complete_task":
		taskID, err := requiredPlanTaskID(envReq.Payload)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		evidence, err := decodePlanEvidence(envReq.Payload)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		task, err := r.plans.CompleteTask(ctx, session.ID, planID, taskID, principal.ID, evidence)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, planTaskMap(planID, task))
	case "block_task":
		taskID, err := requiredPlanTaskID(envReq.Payload)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		reason, _ := envReq.Payload["reason"].(string)
		evidence, err := decodePlanEvidence(envReq.Payload)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		task, err := r.plans.BlockTask(ctx, session.ID, planID, taskID, principal.ID, reason, evidence)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, planTaskMap(planID, task))
	case "replan":
		input, err := decodeReplan(envReq.Payload)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		item, err := r.plans.Replan(ctx, session.ID, planID, principal.ID, input)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, planMap(item))
	case "deliver":
		delivery, err := r.plans.Deliver(ctx, session.ID, planID, principal.ID)
		if err != nil {
			return r.planError(envReq, session, err)
		}
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, deliveryMap(delivery))
	default:
		return r.invalidAction(ctx, req, "plan_manage", action)
	}
}

func decodePlanCreate(payload map[string]any) (plan.CreateInput, error) {
	var input plan.CreateInput
	if err := decodePayload(payload, &input); err != nil {
		return plan.CreateInput{}, fmt.Errorf("%w: invalid create payload: %v", plan.ErrInvalidInput, err)
	}
	return input, nil
}

func decodeReplan(payload map[string]any) (plan.ReplanInput, error) {
	var input plan.ReplanInput
	if err := decodePayload(payload, &input); err != nil {
		return plan.ReplanInput{}, fmt.Errorf("%w: invalid replan payload: %v", plan.ErrInvalidInput, err)
	}
	return input, nil
}

func decodePlanEvidence(payload map[string]any) ([]plan.EvidenceInput, error) {
	value, exists := payload["evidence"]
	if !exists {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid evidence: %v", plan.ErrInvalidInput, err)
	}
	var evidence []plan.EvidenceInput
	if err := json.Unmarshal(encoded, &evidence); err != nil {
		return nil, fmt.Errorf("%w: invalid evidence: %v", plan.ErrInvalidInput, err)
	}
	return evidence, nil
}

func decodePayload(payload map[string]any, target any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func requiredPlanTaskID(payload map[string]any) (string, error) {
	taskID, _ := payload["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return "", fmt.Errorf("%w: task_id is required", plan.ErrInvalidInput)
	}
	return taskID, nil
}

func planMap(item plan.Plan) map[string]any {
	return structMap(item)
}

func planTaskMap(planID string, task plan.Task) map[string]any {
	return map[string]any{"plan_id": planID, "task_id": task.ID, "status": task.Status, "task": task, "evidence": task.Evidence}
}

func deliveryMap(item plan.Delivery) map[string]any {
	return map[string]any{
		"plan_id": item.PlanID, "remote_session_id": item.RemoteSessionID, "status": item.Status,
		"ready": item.Ready, "checks": item.Checks, "blockers": item.Blockers, "plan": item.Plan,
	}
}

func structMap(value any) map[string]any {
	encoded, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	return result
}

func (r *Runtime) planError(envReq envelope.Request, session remotesession.Session, err error) (*mcp.CallToolResult, error) {
	code := "PLAN_ERROR"
	switch {
	case errors.Is(err, plan.ErrNotFound):
		code = "PLAN_NOT_FOUND"
	case errors.Is(err, plan.ErrDependency), errors.Is(err, plan.ErrCycle), errors.Is(err, plan.ErrEvidence), errors.Is(err, plan.ErrEvidenceRequired), errors.Is(err, plan.ErrInvalidInput):
		code = "PLAN_INVALID_REQUEST"
	case errors.Is(err, plan.ErrInvalidState):
		code = "PLAN_STATE_CONFLICT"
	}
	return r.terminalError(envReq, session.ID, session.WorkspaceName, code, err.Error())
}

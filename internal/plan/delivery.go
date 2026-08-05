package plan

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type taskOutcome struct {
	Status   string
	ExitCode int
}

func deliveryChecks(ctx context.Context, tx *sql.Tx, remoteSessionID string, item Plan, now time.Time) ([]DeliveryCheck, []string, error) {
	blockers := make([]string, 0)
	incomplete := make([]string, 0)
	blocked := make([]string, 0)
	for _, task := range item.Tasks {
		if task.Status != TaskCompleted && task.Status != TaskSkipped {
			incomplete = append(incomplete, task.ID+":"+task.Status)
		}
		if task.Status == TaskBlocked {
			blocked = append(blocked, task.ID)
		}
	}
	checks := []DeliveryCheck{{Code: "tasks_complete", Passed: len(incomplete) == 0, Message: "all plan tasks are completed or skipped"}}
	if len(incomplete) != 0 {
		checks[0].Details = map[string]any{"tasks": incomplete}
		blockers = append(blockers, "tasks_incomplete")
	}
	checks = append(checks, DeliveryCheck{Code: "no_blocked_tasks", Passed: len(blocked) == 0, Message: "no plan tasks are blocked"})
	if len(blocked) != 0 {
		checks[len(checks)-1].Details = map[string]any{"tasks": blocked}
		blockers = append(blockers, "tasks_blocked")
	}

	failedVerification := make([]string, 0)
	changesetIDs := make([]string, 0)
	executionTaskIDs := make([]string, 0)
	artifactIDs := make([]string, 0)
	for _, task := range item.Tasks {
		for _, evidence := range task.Evidence {
			if evidenceFailed(evidence) {
				failedVerification = append(failedVerification, evidence.ID)
			}
			switch evidence.Kind {
			case "changeset":
				changesetIDs = append(changesetIDs, evidence.ReferenceID)
			case "execution_task", "task":
				executionTaskIDs = append(executionTaskIDs, evidence.ReferenceID)
			case "artifact":
				artifactIDs = append(artifactIDs, evidence.ReferenceID)
			}
		}
	}
	checks = append(checks, DeliveryCheck{Code: "verification_passed", Passed: len(failedVerification) == 0, Message: "recorded verification evidence has no failure"})
	if len(failedVerification) != 0 {
		checks[len(checks)-1].Details = map[string]any{"evidence": failedVerification}
		blockers = append(blockers, "verification_failed")
	}

	changesets, err := queryStatuses(ctx, tx, "changesets", remoteSessionID, changesetIDs)
	if err != nil {
		return nil, nil, err
	}
	unapplied, err := queryUnappliedChangesets(ctx, tx, remoteSessionID)
	if err != nil {
		return nil, nil, err
	}
	changeBlockers := append([]string{}, unapplied...)
	for _, id := range uniqueStrings(changesetIDs) {
		if changesets[id] != "applied" {
			changeBlockers = append(changeBlockers, "changeset_not_applied:"+id)
		}
	}
	changeBlockers = uniqueStrings(changeBlockers)
	checks = append(checks, DeliveryCheck{Code: "changesets_applied", Passed: len(changeBlockers) == 0, Message: "all Changesets are applied"})
	if len(changeBlockers) != 0 {
		checks[len(checks)-1].Details = map[string]any{"changesets": changeBlockers}
		blockers = append(blockers, changeBlockers...)
	}

	tasks, err := queryTaskOutcomes(ctx, tx, remoteSessionID, executionTaskIDs)
	if err != nil {
		return nil, nil, err
	}
	artifacts, err := queryExistingIDs(ctx, tx, "artifacts", remoteSessionID, artifactIDs)
	if err != nil {
		return nil, nil, err
	}
	executionBlockers := make([]string, 0)
	for _, id := range uniqueStrings(executionTaskIDs) {
		outcome, ok := tasks[id]
		if !ok || outcome.Status != "exited" || outcome.ExitCode != 0 {
			executionBlockers = append(executionBlockers, "execution_task_failed:"+id)
		}
	}
	for _, id := range uniqueStrings(artifactIDs) {
		if !artifacts[id] {
			executionBlockers = append(executionBlockers, "artifact_missing:"+id)
		}
	}
	if len(executionBlockers) != 0 {
		blockers = append(blockers, executionBlockers...)
	}

	var pendingApprovals int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE remote_session_id = ? AND status = 'pending' AND expires_at > ?`, remoteSessionID, now.UnixMilli()).Scan(&pendingApprovals); err != nil {
		return nil, nil, err
	}
	checks = append(checks, DeliveryCheck{Code: "approvals_clear", Passed: pendingApprovals == 0, Message: "no pending approvals remain"})
	if pendingApprovals != 0 {
		checks[len(checks)-1].Details = map[string]any{"pending": pendingApprovals}
		blockers = append(blockers, "pending_approvals")
	}
	if len(executionBlockers) != 0 {
		checks = append(checks, DeliveryCheck{Code: "execution_evidence_valid", Passed: false, Message: "execution task or artifact evidence is incomplete", Details: map[string]any{"items": executionBlockers}})
	} else {
		checks = append(checks, DeliveryCheck{Code: "execution_evidence_valid", Passed: true, Message: "execution and artifact evidence is available"})
	}
	return checks, uniqueStrings(blockers), nil
}

func evidenceFailed(evidence Evidence) bool {
	kind := strings.ToLower(evidence.Kind)
	if kind != "verification" && kind != "test" && kind != "validation" {
		return false
	}
	for _, key := range []string{"status", "result", "outcome"} {
		if value, ok := evidence.Metadata[key].(string); ok {
			value = strings.ToLower(strings.TrimSpace(value))
			if value == "failed" || value == "failure" || value == "error" {
				return true
			}
		}
	}
	if value, ok := evidence.Metadata["passed"].(bool); ok && !value {
		return true
	}
	if value, ok := evidence.Metadata["success"].(bool); ok && !value {
		return true
	}
	return false
}

func queryUnappliedChangesets(ctx context.Context, tx *sql.Tx, remoteSessionID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM changesets WHERE remote_session_id = ? AND status = 'draft' AND discarded_at IS NULL ORDER BY id`, remoteSessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, "changeset_not_applied:"+id)
	}
	return result, rows.Err()
}

func queryStatuses(ctx context.Context, tx *sql.Tx, table, remoteSessionID string, ids []string) (map[string]string, error) {
	result := make(map[string]string)
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return result, nil
	}
	if table != "changesets" {
		return nil, fmt.Errorf("unsupported status table %s", table)
	}
	query := `SELECT id, status FROM changesets WHERE remote_session_id = ? AND id IN (` + placeholders(len(ids)) + `)`
	args := make([]any, 0, len(ids)+1)
	args = append(args, remoteSessionID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, err
		}
		result[id] = status
	}
	return result, rows.Err()
}

func queryTaskOutcomes(ctx context.Context, tx *sql.Tx, remoteSessionID string, ids []string) (map[string]taskOutcome, error) {
	result := make(map[string]taskOutcome)
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return result, nil
	}
	query := `SELECT id, status, COALESCE(exit_code, -1) FROM terminal_tasks WHERE remote_session_id = ? AND id IN (` + placeholders(len(ids)) + `)`
	args := make([]any, 0, len(ids)+1)
	args = append(args, remoteSessionID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		var exitCode int
		if err := rows.Scan(&id, &status, &exitCode); err != nil {
			return nil, err
		}
		result[id] = taskOutcome{Status: status, ExitCode: exitCode}
	}
	return result, rows.Err()
}

func queryExistingIDs(ctx context.Context, tx *sql.Tx, table, remoteSessionID string, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return result, nil
	}
	if table != "artifacts" {
		return nil, fmt.Errorf("unsupported existence table %s", table)
	}
	query := `SELECT id FROM artifacts WHERE remote_session_id = ? AND id IN (` + placeholders(len(ids)) + `)`
	args := make([]any, 0, len(ids)+1)
	args = append(args, remoteSessionID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

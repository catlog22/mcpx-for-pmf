package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        applied_at INTEGER NOT NULL
    );
    CREATE TABLE IF NOT EXISTS runtime_instances (
        id TEXT PRIMARY KEY,
        started_at INTEGER NOT NULL,
        last_seen_at INTEGER NOT NULL
    );
    CREATE TABLE IF NOT EXISTS principals (
        id TEXT PRIMARY KEY,
        kind TEXT NOT NULL,
        subject_hash TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        last_seen_at INTEGER NOT NULL
    );
    CREATE TABLE IF NOT EXISTS remote_sessions (
        id TEXT PRIMARY KEY,
        workspace_name TEXT NOT NULL,
        workspace_path TEXT NOT NULL,
        label TEXT NOT NULL,
        description TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL CHECK (status IN ('active','idle','blocked','closed','archived')),
        owner_principal_id TEXT NOT NULL,
        base_git_head TEXT,
        base_tree_digest TEXT,
        environment_snapshot_id TEXT,
        version INTEGER NOT NULL DEFAULT 1,
        created_at INTEGER NOT NULL,
        last_active_at INTEGER NOT NULL,
        closed_at INTEGER,
        FOREIGN KEY (owner_principal_id) REFERENCES principals(id)
    );
    CREATE TABLE IF NOT EXISTS remote_session_members (
        remote_session_id TEXT NOT NULL,
        principal_id TEXT NOT NULL,
        role TEXT NOT NULL CHECK (role IN ('viewer','editor','approver','owner')),
        joined_at INTEGER NOT NULL,
        last_active_at INTEGER NOT NULL,
        PRIMARY KEY (remote_session_id, principal_id),
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE,
        FOREIGN KEY (principal_id) REFERENCES principals(id) ON DELETE CASCADE
    );
    CREATE TABLE IF NOT EXISTS remote_session_clients (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        remote_session_id TEXT NOT NULL,
        principal_id TEXT NOT NULL,
        client_name TEXT NOT NULL,
        client_version TEXT NOT NULL,
        first_seen_at INTEGER NOT NULL,
        last_seen_at INTEGER NOT NULL,
        UNIQUE (remote_session_id, principal_id, client_name, client_version),
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE TABLE IF NOT EXISTS remote_session_handoffs (
        id TEXT PRIMARY KEY,
        remote_session_id TEXT NOT NULL,
        token_hash TEXT NOT NULL UNIQUE,
        role TEXT NOT NULL CHECK (role IN ('viewer','editor','approver')),
        created_by TEXT NOT NULL,
        note TEXT NOT NULL DEFAULT '',
        created_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL,
        consumed_at INTEGER,
        consumed_by TEXT,
        revoked_at INTEGER,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE TABLE IF NOT EXISTS remote_session_events (
        sequence INTEGER PRIMARY KEY AUTOINCREMENT,
        remote_session_id TEXT NOT NULL,
        principal_id TEXT,
        client_name TEXT,
        event_type TEXT NOT NULL,
        operation_id TEXT,
        summary TEXT NOT NULL,
        resource_uri TEXT,
        metadata_json TEXT NOT NULL DEFAULT '{}',
        created_at INTEGER NOT NULL,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE TABLE IF NOT EXISTS environment_snapshots (
        id TEXT PRIMARY KEY,
        remote_session_id TEXT,
        runtime_instance_id TEXT NOT NULL,
        static_digest TEXT NOT NULL,
        snapshot_json TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE TABLE IF NOT EXISTS idempotency_records (
        remote_session_id TEXT NOT NULL,
        principal_id TEXT NOT NULL,
        client_request_id TEXT NOT NULL,
        operation TEXT NOT NULL,
        response_json TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL,
        PRIMARY KEY (remote_session_id, principal_id, client_request_id, operation)
    );
    CREATE INDEX IF NOT EXISTS idx_remote_sessions_workspace_activity
        ON remote_sessions(workspace_name, last_active_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_remote_members_principal
        ON remote_session_members(principal_id, last_active_at DESC);
    CREATE INDEX IF NOT EXISTS idx_remote_events_session_sequence
        ON remote_session_events(remote_session_id, sequence DESC);
    CREATE INDEX IF NOT EXISTS idx_remote_handoffs_expiry
        ON remote_session_handoffs(expires_at)
        WHERE consumed_at IS NULL AND revoked_at IS NULL;`,
	`CREATE TABLE IF NOT EXISTS changesets (
        id TEXT PRIMARY KEY,
        remote_session_id TEXT NOT NULL,
        created_by TEXT NOT NULL,
        status TEXT NOT NULL CHECK (status IN ('draft','applied','reverted','failed')),
        summary TEXT NOT NULL DEFAULT '',
        digest TEXT NOT NULL,
        source_changeset_id TEXT,
        created_at INTEGER NOT NULL,
        applied_at INTEGER,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE,
        FOREIGN KEY (source_changeset_id) REFERENCES changesets(id)
    );
    CREATE TABLE IF NOT EXISTS changeset_files (
        changeset_id TEXT NOT NULL,
        ordinal INTEGER NOT NULL,
        operation TEXT NOT NULL CHECK (operation IN ('update','create','rename','delete')),
        path TEXT NOT NULL,
        new_path TEXT NOT NULL DEFAULT '',
        expected_sha256 TEXT NOT NULL DEFAULT '',
        original_sha256 TEXT NOT NULL DEFAULT '',
        proposed_sha256 TEXT NOT NULL DEFAULT '',
        original_mode INTEGER NOT NULL DEFAULT 0,
        original_content BLOB,
        proposed_content BLOB,
        PRIMARY KEY (changeset_id, ordinal),
        FOREIGN KEY (changeset_id) REFERENCES changesets(id) ON DELETE CASCADE
    );
    CREATE TABLE IF NOT EXISTS change_journals (
        id TEXT PRIMARY KEY,
        changeset_id TEXT NOT NULL,
        status TEXT NOT NULL CHECK (status IN ('applying','completed','rolled_back','failed')),
        journal_json TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL,
        FOREIGN KEY (changeset_id) REFERENCES changesets(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_changesets_session_created
        ON changesets(remote_session_id, created_at DESC, id DESC);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_change_journals_once
        ON change_journals(changeset_id);`,
	`CREATE TABLE IF NOT EXISTS workspace_baselines (
        remote_session_id TEXT PRIMARY KEY,
        git_head TEXT NOT NULL DEFAULT '',
        captured_at INTEGER NOT NULL,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE TABLE IF NOT EXISTS workspace_baseline_files (
        remote_session_id TEXT NOT NULL,
        path TEXT NOT NULL,
        status TEXT NOT NULL,
        PRIMARY KEY (remote_session_id, path),
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_workspace_baseline_files_session
        ON workspace_baseline_files(remote_session_id, path);`,
	`CREATE TABLE IF NOT EXISTS terminal_tasks (
        id TEXT PRIMARY KEY,
        remote_session_id TEXT NOT NULL,
        workspace_name TEXT NOT NULL,
        workspace_path TEXT NOT NULL,
        command TEXT NOT NULL,
        status TEXT NOT NULL CHECK (status IN ('running','exited','killed','interrupted','failed')),
        pid INTEGER NOT NULL DEFAULT 0,
        exit_code INTEGER,
        log_path TEXT NOT NULL,
        log_size INTEGER NOT NULL DEFAULT 0,
        log_truncated INTEGER NOT NULL DEFAULT 0,
        started_at INTEGER NOT NULL,
        finished_at INTEGER,
        updated_at INTEGER NOT NULL,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_terminal_tasks_session_started
        ON terminal_tasks(remote_session_id, started_at DESC, id DESC);`,
	`CREATE TABLE IF NOT EXISTS approvals (
        id TEXT PRIMARY KEY,
        remote_session_id TEXT NOT NULL,
        principal_id TEXT NOT NULL,
        tool TEXT NOT NULL,
        summary TEXT NOT NULL,
        payload_json TEXT NOT NULL,
        status TEXT NOT NULL CHECK (status IN ('pending','consumed','expired')),
        created_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL,
        consumed_at INTEGER,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_approvals_remote_pending
        ON approvals(remote_session_id, created_at DESC) WHERE status = 'pending';`,
	`CREATE TABLE IF NOT EXISTS file_snapshots (
        id TEXT PRIMARY KEY,
        remote_session_id TEXT NOT NULL,
        snapshot_json TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_file_snapshots_session_created
        ON file_snapshots(remote_session_id, created_at DESC);`,
	`CREATE TABLE IF NOT EXISTS secret_requests (
        id TEXT PRIMARY KEY,
        remote_session_id TEXT NOT NULL,
        principal_id TEXT NOT NULL,
        payload_json TEXT NOT NULL,
        status TEXT NOT NULL CHECK (status IN ('pending','consumed','expired')),
        created_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL,
        consumed_at INTEGER,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_secret_requests_remote_pending
        ON secret_requests(remote_session_id, created_at DESC) WHERE status = 'pending';`,
	`CREATE TABLE IF NOT EXISTS artifacts (
        id TEXT PRIMARY KEY,
        remote_session_id TEXT NOT NULL,
        name TEXT NOT NULL,
        kind TEXT NOT NULL,
        path TEXT NOT NULL,
        mime_type TEXT NOT NULL,
        size INTEGER NOT NULL,
        sha256 TEXT NOT NULL,
        created_by TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_artifacts_session_created
        ON artifacts(remote_session_id, created_at DESC, id DESC);`,
	`CREATE TABLE IF NOT EXISTS plans (
        id TEXT PRIMARY KEY,
        remote_session_id TEXT NOT NULL,
        goal TEXT NOT NULL,
        summary TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL CHECK (status IN ('ready','in_progress','blocked','completed','cancelled')),
        version INTEGER NOT NULL DEFAULT 1,
        created_by TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL,
        completed_at INTEGER,
        FOREIGN KEY (remote_session_id) REFERENCES remote_sessions(id) ON DELETE CASCADE
    );
    CREATE TABLE IF NOT EXISTS plan_tasks (
        id TEXT PRIMARY KEY,
        plan_id TEXT NOT NULL,
        ordinal INTEGER NOT NULL,
        title TEXT NOT NULL,
        description TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL CHECK (status IN ('todo','in_progress','blocked','completed','skipped')),
        depends_on_json TEXT NOT NULL DEFAULT '[]',
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL,
        completed_at INTEGER,
        FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE CASCADE,
        UNIQUE (plan_id, ordinal)
    );
    CREATE TABLE IF NOT EXISTS plan_task_evidence (
        id TEXT PRIMARY KEY,
        plan_id TEXT NOT NULL,
        task_id TEXT NOT NULL,
        kind TEXT NOT NULL,
        reference_id TEXT NOT NULL,
        metadata_json TEXT NOT NULL DEFAULT '{}',
        created_by TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE CASCADE,
        FOREIGN KEY (task_id) REFERENCES plan_tasks(id) ON DELETE CASCADE,
        UNIQUE (task_id, kind, reference_id)
    );
    CREATE TABLE IF NOT EXISTS plan_events (
        id TEXT PRIMARY KEY,
        plan_id TEXT NOT NULL,
        task_id TEXT,
        event_type TEXT NOT NULL,
        reason TEXT NOT NULL DEFAULT '',
        payload_json TEXT NOT NULL DEFAULT '{}',
        created_by TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE CASCADE,
        FOREIGN KEY (task_id) REFERENCES plan_tasks(id) ON DELETE SET NULL
    );
    CREATE INDEX IF NOT EXISTS idx_plans_session_updated
        ON plans(remote_session_id, updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_plan_tasks_plan_ordinal
        ON plan_tasks(plan_id, ordinal, id);
    CREATE INDEX IF NOT EXISTS idx_plan_evidence_plan_task
        ON plan_task_evidence(plan_id, task_id, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_plan_events_plan_created
        ON plan_events(plan_id, created_at, id);`,
	`ALTER TABLE approvals ADD COLUMN content_key TEXT NOT NULL DEFAULT '';
	CREATE UNIQUE INDEX IF NOT EXISTS idx_approvals_content_key
		ON approvals(remote_session_id, content_key) WHERE status = 'pending' AND content_key <> '';`,
	`CREATE TABLE IF NOT EXISTS observation_events (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		workspace_name TEXT NOT NULL,
		remote_session_id TEXT NOT NULL DEFAULT '',
		request_id TEXT NOT NULL DEFAULT '',
		operation_id TEXT NOT NULL DEFAULT '',
		tool_name TEXT NOT NULL DEFAULT '',
		event_type TEXT NOT NULL,
		intent TEXT NOT NULL DEFAULT '',
		input_json TEXT NOT NULL DEFAULT '{}',
		output_json TEXT NOT NULL DEFAULT '{}',
		summary TEXT NOT NULL DEFAULT '',
		resource_uri TEXT NOT NULL DEFAULT '',
		stream TEXT NOT NULL DEFAULT '',
		stream_offset INTEGER NOT NULL DEFAULT 0,
		truncated INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_observation_events_workspace_sequence
		ON observation_events(workspace_name, sequence);`,
	`ALTER TABLE observation_events ADD COLUMN progress_summary TEXT NOT NULL DEFAULT '';`,
	`CREATE INDEX IF NOT EXISTS idx_observation_events_retention
		ON observation_events(workspace_name, event_type, created_at, sequence);
	CREATE INDEX IF NOT EXISTS idx_terminal_tasks_retention
		ON terminal_tasks(status, finished_at, updated_at, remote_session_id);
	CREATE INDEX IF NOT EXISTS idx_file_snapshots_retention
		ON file_snapshots(created_at, remote_session_id);
	CREATE INDEX IF NOT EXISTS idx_environment_snapshots_retention
		ON environment_snapshots(created_at, remote_session_id);`,
	`ALTER TABLE changeset_files ADD COLUMN deleted_files INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE changeset_files ADD COLUMN deleted_directories INTEGER NOT NULL DEFAULT 0;`,
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        applied_at INTEGER NOT NULL
    )`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	for i, migration := range migrations {
		version := i + 1
		var exists int
		err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("read migration %d: %w", version, err)
		}
		if exists != 0 {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)", version, time.Now().UTC().UnixMilli()); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

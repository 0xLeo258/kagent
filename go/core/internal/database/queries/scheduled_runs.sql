-- name: CreateScheduledRunBinding :one
INSERT INTO scheduled_run_binding (scheduled_run_namespace, scheduled_run_name, scheduled_run_uid, bound_user_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (scheduled_run_uid) DO NOTHING
RETURNING *;

-- name: GetScheduledRunBinding :one
SELECT * FROM scheduled_run_binding
WHERE scheduled_run_namespace = $1 AND scheduled_run_name = $2 AND scheduled_run_uid = $3;

-- name: CreateScheduledRunExecution :one
INSERT INTO scheduled_run_execution (
    id, scheduled_run_namespace, scheduled_run_name, scheduled_run_uid,
    start_time, deadline, trigger, prompt, user_id, phase, status
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'Creating', 'InProgress')
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: GetScheduledRunExecution :one
SELECT * FROM scheduled_run_execution WHERE id = $1;

-- name: GetScheduledRunExecutionByAgentInstanceID :one
SELECT e.* FROM scheduled_run_execution e
WHERE e.agent_instance_id = $1
    OR (e.agent_instance_id IS NULL AND EXISTS (
        SELECT 1 FROM agent_instance i WHERE i.id = $1 AND e.id = i.request_id
            AND e.user_id = i.user_id
    ));

-- name: UpdateScheduledRunExecution :execrows
UPDATE scheduled_run_execution SET
    agent_instance_id = sqlc.narg(agent_instance_id),
    task_id = sqlc.narg(task_id),
    phase = sqlc.arg(phase),
    status = sqlc.arg(status),
    status_message = sqlc.arg(status_message),
    completion_time = sqlc.narg(completion_time),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
    AND user_id = sqlc.arg(user_id)
    AND status = 'InProgress'
    AND (agent_instance_id IS NULL OR agent_instance_id = sqlc.narg(agent_instance_id))
    AND (task_id IS NULL OR task_id = sqlc.narg(task_id))
    AND CASE phase WHEN 'Creating' THEN 0 WHEN 'Dispatching' THEN 1 WHEN 'Polling' THEN 2 ELSE 3 END
        <= CASE sqlc.arg(phase)::text WHEN 'Creating' THEN 0 WHEN 'Dispatching' THEN 1 WHEN 'Polling' THEN 2 ELSE 3 END;

-- name: ListScheduledRunExecutions :many
SELECT * FROM scheduled_run_execution
WHERE scheduled_run_namespace = sqlc.arg(scheduled_run_namespace)
    AND scheduled_run_name = sqlc.arg(scheduled_run_name)
    AND scheduled_run_uid = sqlc.arg(scheduled_run_uid)
    AND (sqlc.narg(before_time)::timestamptz IS NULL
         OR (start_time, id) < (sqlc.narg(before_time)::timestamptz, sqlc.arg(before_id)::text))
ORDER BY start_time DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListInProgressScheduledRunExecutions :many
SELECT * FROM scheduled_run_execution WHERE status = 'InProgress'
ORDER BY start_time, id;

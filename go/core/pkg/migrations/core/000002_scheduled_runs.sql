-- +goose Up
CREATE TABLE scheduled_run_binding (
    scheduled_run_uid TEXT PRIMARY KEY CHECK (scheduled_run_uid <> ''),
    scheduled_run_namespace TEXT NOT NULL CHECK (scheduled_run_namespace <> ''),
    scheduled_run_name TEXT NOT NULL CHECK (scheduled_run_name <> ''),
    bound_user_id TEXT NOT NULL CHECK (bound_user_id <> '' AND bound_user_id <> 'scheduled-run')
);

CREATE TABLE scheduled_run_execution (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    scheduled_run_namespace TEXT NOT NULL CHECK (scheduled_run_namespace <> ''),
    scheduled_run_name TEXT NOT NULL CHECK (scheduled_run_name <> ''),
    scheduled_run_uid TEXT NOT NULL CHECK (scheduled_run_uid <> ''),
    user_id TEXT NOT NULL CHECK (user_id <> ''),
    start_time TIMESTAMPTZ NOT NULL,
    deadline TIMESTAMPTZ NOT NULL CHECK (deadline > start_time),
    completion_time TIMESTAMPTZ,
    trigger TEXT NOT NULL CHECK (trigger IN ('Scheduled', 'Manual')),
    agent_instance_id UUID,
    task_id TEXT,
    status TEXT NOT NULL CHECK (status IN ('DispatchFailed', 'InProgress', 'Succeeded', 'Failed', 'TimedOut')),
    status_message TEXT NOT NULL DEFAULT '',
    prompt TEXT NOT NULL CHECK (length(prompt) BETWEEN 1 AND 32768),
    phase TEXT NOT NULL CHECK (phase IN ('Creating', 'Dispatching', 'Polling', 'Complete')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((phase = 'Complete') = (status <> 'InProgress')),
    CHECK ((phase = 'Complete') = (completion_time IS NOT NULL)),
    CHECK (phase NOT IN ('Dispatching', 'Polling') OR agent_instance_id IS NOT NULL),
    CHECK (phase <> 'Polling' OR task_id IS NOT NULL)
);

CREATE UNIQUE INDEX scheduled_run_execution_instance ON scheduled_run_execution (agent_instance_id)
    WHERE agent_instance_id IS NOT NULL;
CREATE INDEX scheduled_run_execution_history ON scheduled_run_execution
    (scheduled_run_namespace, scheduled_run_name, scheduled_run_uid, start_time DESC, id DESC);
CREATE INDEX scheduled_run_execution_in_progress ON scheduled_run_execution (start_time, id)
    WHERE status = 'InProgress';

-- +goose Down
DROP TABLE scheduled_run_execution;
DROP TABLE scheduled_run_binding;

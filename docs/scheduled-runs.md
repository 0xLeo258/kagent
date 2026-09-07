# Scheduled runs

A `kagent.dev/v1alpha3` ScheduledRun sends a prompt to an AgentTemplate on a
five-field cron schedule. Each execution creates its own AgentInstance using
the selected Harness. Both references must exist in the same namespace and
are immutable after creation.

```yaml
apiVersion: kagent.dev/v1alpha3
kind: ScheduledRun
metadata:
  name: daily-summary
  namespace: kagent
spec:
  schedule: "0 9 * * 1-5"
  timeZone: Asia/Shanghai
  targetRef:
    apiGroup: kagent.dev
    kind: AgentTemplate
    name: assistant
  harnessRef:
    name: kagent
  prompt: Summarize the current cluster status and highlight issues to investigate.
  executionTimeout: 15m
  recentExecutionsLimit: 10
  suspended: false
  allowSessionInteraction: false
```

Install the updated CRD chart before the controller chart, then apply the
resource with `kubectl apply -f schedule.yaml`. The controller also needs read
and write access to ScheduledRuns and their status; the Helm chart supplies it.
Only namespaces selected by `WATCH_NAMESPACES` are processed.

In the UI, open **Schedules** to create or edit schedules, pause automatic
execution, trigger a run immediately, and browse execution history. A manual
trigger works while the schedule is paused. Each accepted trigger appears as
`InProgress` immediately; its conversation link becomes available once the
AgentInstance has been created. The same operations are exposed by the gRPC
`kagent.api.v1alpha1.ScheduledRunService`.

## Timing and outcomes

The default time zone is UTC. Cron uses five fields, from minute through day of
week; expressions with seconds and `@every` are unsupported. Invalid cron or
time-zone values produce an `Accepted=False` condition. Pausing prevents new
automatic ticks and does not cancel work already accepted. Executions may
overlap. Ticks missed while the controller is unavailable are not replayed.

`executionTimeout` defaults to 15 minutes and includes queueing, instance
creation, dispatch, and waiting for the task. Outcomes are `Succeeded`, `Failed`,
`DispatchFailed`, or `TimedOut`. Tasks awaiting human input remain in progress
until continued or timed out. On timeout the scheduler attempts to cancel a
known task; cancellation failure is logged and does not erase the timeout.

The elected controller processes durable execution requests, including requests
accepted by another API replica. It stores the prompt and deadline at acceptance,
uses stable instance-creation and message identifiers, and recovers existing
tasks after a restart. Recovery checks an existing task's final state before
classifying an elapsed deadline. Execution writes are fenced by the ScheduledRun
UID so a replacement with the same name cannot inherit an old execution.
If the runtime reports that an interrupted dispatch has no task, recovery can
mark it `Failed`; this does not promise exactly-once execution across failures.

The scheduler's leadership and durable queue do not change the current A2A
gateway deployment restriction: keep the controller/gateway at one replica
while its runtime coordination remains process-local.

Enable the chart's existing metrics endpoint with
`controller.metrics.enabled=true` to scrape active schedules, execution
outcomes, and execution duration. Metrics are disabled by default. The default
endpoint uses HTTPS and Kubernetes authentication and authorization; bind the
chart's metrics-reader ClusterRole to the scraper's ServiceAccount. The chart
configures `METRICS_BIND_ADDRESS` and `METRICS_SECURE` for the controller and
supplies its authentication RBAC.

## History and conversation access

The Kubernetes status is a summary: up to 100 active executions and
`recentExecutionsLimit` completed executions (default 10, maximum 100). Status
messages in that summary are truncated to 1,024 characters. Full execution
history and longer messages remain in PostgreSQL and are available through
cursor pagination. Summary pruning does not delete execution history.

Scheduled conversations are reached from execution history and are excluded
from ordinary conversation lists. A caller must be authenticated and authorized
to read the owning ScheduledRun. Conversations are read-only by default.
Setting `allowSessionInteraction: true` permits those readers to send messages,
answer requests for input, cancel tasks, and resume or suspend the instance.
The server checks the current policy for each new request. Schedule readers
cannot rename or delete these conversations or create independent share links.

Deleting a ScheduledRun stops future dispatch and removes access through that
schedule. It does not purge persisted execution history or AgentInstances.
Recreating its name creates a new history and grants no access to the old
conversations. The internal owner identity `scheduled-run` cannot be used to
authenticate public conversation or control-plane operations. Runtime memory
callbacks retain their existing identity and access semantics.

## Validation

Focused scheduler tests cover crash recovery, ownership isolation, timeouts,
leader dispatch, and status reconstruction. PostgreSQL tests cover migrations,
idempotent transitions, and stable history pagination. Kubernetes envtest checks
the generated CRD's defaults and admission rules. The gRPC tests exercise
schedule CRUD and conversation authorization through registered services.

For the runtime integration test, use the clean Substrate-backed installation
described in [the E2E guide](../go/core/test/e2e/README.md), then run:

```bash
KAGENT_E2E_API_URL=http://localhost:8083 \
  go -C go test ./core/test/e2e -run TestScheduledRunInteraction -v
```

This test uses the mock LLM and verifies manual execution while paused,
independent conversations, paginated history, read-only access, continuation
after a policy update, and isolation after deleting and recreating a schedule.

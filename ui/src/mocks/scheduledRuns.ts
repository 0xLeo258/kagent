import type {
  ScheduledRun,
  ScheduledRunExecution,
  ScheduledRunResource,
} from "@/api/domain/scheduledRuns";

export function fixtureScheduledRun(name = "daily-report"): ScheduledRun {
  return {
    namespace: "kagent",
    name,
    resource: {
      metadata: { namespace: "kagent", name },
      spec: {
        targetRef: {
          apiGroup: "kagent.dev",
          kind: "AgentTemplate",
          name: "k8s-agent-7f3a91c",
        },
        harnessRef: { name: "k8s-agent" },
        schedule: "0 9 * * *",
        timeZone: "UTC",
        prompt: "Summarize cluster health.",
        suspended: false,
        allowSessionInteraction: false,
        executionTimeout: "15m",
        recentExecutionsLimit: 10,
      },
      status: { conditions: [{ type: "Accepted", status: "True" }] },
    },
  };
}

const schedules = new Map(
  [fixtureScheduledRun(), fixtureScheduledRun("removable-run")].map((run) => [
    `${run.namespace}/${run.name}`,
    run,
  ]),
);
export const SCHEDULED_INSTANCE_ID = "a4138a6b-3c7a-4d1e-a201-a64cbe3f72a0";
const executions = new Map<string, ScheduledRunExecution[]>([
  [
    "kagent/daily-report",
    [
      {
        id: "scheduled-fixture-execution",
        agentInstanceId: SCHEDULED_INSTANCE_ID,
        startTime: "2026-09-07T09:00:00Z",
        completionTime: "2026-09-07T09:00:05Z",
        trigger: "Scheduled",
        status: "Succeeded",
      },
    ],
  ],
]);
export const allScheduledRuns = () => [...schedules.values()];
export const findScheduledRun = (ref: string) => schedules.get(ref);
export function saveScheduledRun(
  namespace: string,
  name: string,
  resource: ScheduledRunResource,
): ScheduledRun {
  const run = {
    namespace,
    name,
    resource: {
      ...resource,
      metadata: { ...resource.metadata, namespace, name },
      status: { conditions: [{ type: "Accepted", status: "True" }] },
    },
  };
  schedules.set(`${namespace}/${name}`, run);
  return run;
}
export function removeScheduledRun(ref: string) {
  schedules.delete(ref);
  executions.delete(ref);
}
export const scheduledExecutions = (ref: string) => executions.get(ref) ?? [];
export function recordScheduledExecution(
  ref: string,
  execution: ScheduledRunExecution,
) {
  executions.set(ref, [execution, ...scheduledExecutions(ref)]);
}

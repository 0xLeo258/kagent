import type {
  ScheduledRun,
  ScheduledRunExecution,
  ScheduledRunResource,
} from "@/api/domain/scheduledRuns";
import { MOCK_INSTANCE_CREATOR } from "./fixtures";

export function fixtureScheduledRun(name = "daily-report", boundUserId?: string): ScheduledRun {
  return {
    namespace: "kagent",
    name,
    boundUserId,
    resource: {
      metadata: { namespace: "kagent", name, uid: `mock-schedule-${name}`, generation: 1 },
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
        executionTimeout: "15m",
        recentExecutionsLimit: 10,
      },
      status: { conditions: [{ type: "Accepted", status: "True" }] },
    },
  };
}

export const SCHEDULED_CONVERSATIONS = [
  { name: "daily-report", id: "a4138a6b-3c7a-4d1e-a201-a64cbe3f72a0", boundUserId: undefined },
  { name: "owned-report", id: "bcf76189-4dc6-4acd-baa1-4e811b8f3647", boundUserId: MOCK_INSTANCE_CREATOR },
  { name: "shared-report", id: "5fb7c6b1-5312-4c2a-84a3-e65844766422", boundUserId: "bob@example.com" },
];
const schedules = new Map(
  [
    ...SCHEDULED_CONVERSATIONS.map(({ name, boundUserId }) => fixtureScheduledRun(name, boundUserId)),
    fixtureScheduledRun("removable-run"),
  ].map((run) => [`${run.namespace}/${run.name}`, run]),
);
const executions = new Map<string, ScheduledRunExecution[]>(
  SCHEDULED_CONVERSATIONS.map(({ name, id }) => [
    `kagent/${name}`,
    [
      {
        id: `scheduled-fixture-${name}`,
        agentInstanceId: id,
        startTime: "2026-09-07T09:00:00Z",
        completionTime: "2026-09-07T09:00:05Z",
        trigger: "Scheduled",
        status: "Succeeded",
      },
    ],
  ]),
);
export const allScheduledRuns = () => [...schedules.values()];
export const findScheduledRun = (ref: string) => schedules.get(ref);
export function scheduledRunForInstance(instanceID: string): ScheduledRun | undefined {
  const entry = [...executions].find(([, rows]) => rows.some((row) => row.agentInstanceId === instanceID));
  return entry ? findScheduledRun(entry[0]) : undefined;
}
export const scheduledRunMayReply = (run: ScheduledRun | undefined) =>
  run?.boundUserId === MOCK_INSTANCE_CREATOR;
export function saveScheduledRun(
  namespace: string,
  name: string,
  resource: ScheduledRunResource,
): ScheduledRun {
  const existing = findScheduledRun(`${namespace}/${name}`);
  const specChanged = existing && JSON.stringify(existing.resource.spec) !== JSON.stringify(resource.spec);
  const run = {
    namespace,
    name,
    boundUserId: existing ? existing.boundUserId : MOCK_INSTANCE_CREATOR,
    resource: {
      ...resource,
      metadata: {
        ...resource.metadata,
        namespace,
        name,
        uid: existing?.resource.metadata.uid ?? crypto.randomUUID(),
        generation: existing
          ? (existing.resource.metadata.generation ?? 1) + (specChanged ? 1 : 0)
          : 1,
      },
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

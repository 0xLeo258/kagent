import type { ResourceMetadata } from "./common";

/** The ScheduledRun v1alpha3 spec; both references are local and immutable. */
export interface ScheduledRunSpec {
  schedule: string;
  timeZone?: string;
  targetRef: { apiGroup: "kagent.dev"; kind: "AgentTemplate"; name: string };
  harnessRef: { name: string };
  prompt: string;
  suspended?: boolean;
  executionTimeout?: string;
  recentExecutionsLimit?: number;
}

export interface ScheduledRunResource {
  metadata: ResourceMetadata;
  spec: ScheduledRunSpec;
  status?: {
    lastExecutionTime?: string;
    nextExecutionTime?: string;
    observedGeneration?: number;
    conditions?: Array<{
      type: string;
      status: string;
      reason?: string;
      message?: string;
      observedGeneration?: number;
    }>;
  };
}

export interface ScheduledRun {
  namespace: string;
  name: string;
  /** Read-only binding resolved by the server; absent for unbound schedules. */
  boundUserId?: string;
  resource: ScheduledRunResource;
}

/** Durable execution history, normalised from the typed gRPC response. */
export interface ScheduledRunExecution {
  id: string;
  startTime: string;
  completionTime?: string;
  trigger: string;
  agentInstanceId?: string;
  taskId?: string;
  status: string;
  statusMessage?: string;
}

export interface ScheduledRunExecutionPage {
  executions: ScheduledRunExecution[];
  nextPageToken?: string;
}

export interface ScheduledRunWrite {
  namespace: string;
  name: string;
  resource: ScheduledRunResource;
}

export function scheduledRunStatus(run: ScheduledRun): {
  label: string;
  color: string;
  message?: string;
} {
  const accepted = run.resource.status?.conditions?.find(
    (entry) => entry.type === "Accepted",
  );
  if (accepted?.status === "False") {
    return {
      label: "Rejected",
      color: "error",
      message: accepted.message ?? accepted.reason,
    };
  }
  if (run.resource.spec.suspended)
    return { label: "Suspended", color: "default" };
  if (accepted?.status !== "True")
    return { label: "Pending", color: "warning" };
  return { label: "Active", color: "success" };
}

/** An update carries the spec's original identity and generation for conflict detection. */
export function scheduledRunWrite(
  run: ScheduledRun,
  spec = run.resource.spec,
): ScheduledRunWrite {
  const { labels, annotations, uid, generation } = run.resource.metadata;
  return {
    namespace: run.namespace,
    name: run.name,
    resource: {
      metadata: {
        namespace: run.namespace,
        name: run.name,
        labels,
        annotations,
        uid,
        generation,
      },
      spec,
    },
  };
}

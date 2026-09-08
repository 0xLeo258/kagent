import type {
  ScheduledRun,
  ScheduledRunWrite,
} from "@/api/domain/scheduledRuns";
import { scheduledRunWrite } from "@/api/domain/scheduledRuns";
import {
  DEFAULT_NAMESPACE,
  RFC1123_SUBDOMAIN,
} from "@/components/common/resourceName";

export interface ScheduledRunDraft {
  name: string;
  namespace: string;
  agentTemplate: string;
  harness: string;
  schedule: string;
  timeZone: string;
  prompt: string;
  suspended: boolean;
  executionTimeout: string;
  recentExecutionsLimit: number;
}

export function emptyScheduledRunDraft(
  namespace = DEFAULT_NAMESPACE,
): ScheduledRunDraft {
  return {
    name: "",
    namespace,
    agentTemplate: "",
    harness: "",
    schedule: "",
    timeZone: "UTC",
    prompt: "",
    suspended: false,
    executionTimeout: "15m",
    recentExecutionsLimit: 10,
  };
}

export function scheduledRunDraftFrom(run: ScheduledRun): ScheduledRunDraft {
  const spec = run.resource.spec;
  return {
    name: run.name,
    namespace: run.namespace,
    agentTemplate: spec.targetRef.name,
    harness: spec.harnessRef.name,
    schedule: spec.schedule,
    timeZone: spec.timeZone ?? "UTC",
    prompt: spec.prompt,
    suspended: spec.suspended ?? false,
    executionTimeout: spec.executionTimeout ?? "15m",
    recentExecutionsLimit: spec.recentExecutionsLimit ?? 10,
  };
}

export function scheduledRunDraftIssues(draft: ScheduledRunDraft): string[] {
  const issues: string[] = [];
  const labels = draft.name.trim().split(".");
  if (
    !draft.name.trim() ||
    draft.name.trim().length > 253 ||
    !labels.every((label) => RFC1123_SUBDOMAIN.test(label))
  )
    issues.push(
      "Enter a valid schedule name (lowercase letters, numbers, hyphens or dots; up to 253 characters).",
    );
  if (
    !draft.namespace ||
    draft.namespace.length > 63 ||
    !RFC1123_SUBDOMAIN.test(draft.namespace)
  )
    issues.push("Select a valid namespace.");
  if (!draft.agentTemplate || !draft.harness)
    issues.push("Select an agent template and harness.");
  if (draft.schedule.trim().split(/\s+/).length !== 5)
    issues.push(
      "Use a five-field cron schedule: minute hour day month weekday.",
    );
  if (!draft.prompt.trim()) issues.push("Enter a prompt.");
  if ([...draft.prompt].length > 32768)
    issues.push("The prompt must not exceed 32,768 characters.");
  try {
    new Intl.DateTimeFormat("en", { timeZone: draft.timeZone.trim() || "UTC" });
  } catch {
    issues.push("Enter an IANA time zone, such as UTC or Asia/Shanghai.");
  }
  if (
    !/^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/.test(
      draft.executionTimeout.trim(),
    ) ||
    !/[1-9]/.test(draft.executionTimeout)
  )
    issues.push("Enter a positive execution timeout, such as 15m or 1h30m.");
  if (
    !Number.isInteger(draft.recentExecutionsLimit) ||
    draft.recentExecutionsLimit < 1 ||
    draft.recentExecutionsLimit > 100
  )
    issues.push("Recent completed executions must be between 1 and 100.");
  return issues;
}

export function scheduledRunPayloadFrom(
  draft: ScheduledRunDraft,
  existing?: ScheduledRun,
): ScheduledRunWrite {
  const spec = {
    ...existing?.resource.spec,
    targetRef: existing?.resource.spec.targetRef ?? {
      apiGroup: "kagent.dev" as const,
      kind: "AgentTemplate" as const,
      name: draft.agentTemplate,
    },
    harnessRef: existing?.resource.spec.harnessRef ?? { name: draft.harness },
    schedule: draft.schedule.trim(),
    timeZone: draft.timeZone.trim() || "UTC",
    prompt: draft.prompt,
    suspended: draft.suspended,
    executionTimeout: draft.executionTimeout.trim(),
    recentExecutionsLimit: draft.recentExecutionsLimit,
  };
  if (existing) return scheduledRunWrite(existing, spec);
  return {
    name: draft.name.trim(),
    namespace: draft.namespace,
    resource: {
      metadata: { name: draft.name.trim(), namespace: draft.namespace },
      spec,
    },
  };
}

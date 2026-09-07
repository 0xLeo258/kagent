import { describe, expect, it } from "vitest";
import { fixtureScheduledRun } from "@/mocks/scheduledRuns";
import {
  emptyScheduledRunDraft,
  scheduledRunDraftFrom,
  scheduledRunDraftIssues,
  scheduledRunPayloadFrom,
} from "./scheduledRunDraft";

describe("schedule authoring", () => {
  it("defaults conversations to read-only and requires a complete target", () => {
    const draft = emptyScheduledRunDraft();
    expect(draft.allowSessionInteraction).toBe(false);
    expect(scheduledRunDraftIssues(draft)).toContain(
      "Select an agent template and harness.",
    );
    expect(
      scheduledRunDraftIssues(scheduledRunDraftFrom(fixtureScheduledRun())),
    ).toEqual([]);
  });

  it.each([
    ["schedule", "* * *", "five-field"],
    ["timeZone", "Not/AZone", "IANA"],
    ["executionTimeout", "0s", "positive"],
    ["executionTimeout", "-15m", "positive"],
    ["recentExecutionsLimit", 101, "between 1 and 100"],
    ["prompt", "  ", "Enter a prompt"],
  ])("rejects invalid %s", (key, value, expected) => {
    expect(
      scheduledRunDraftIssues({
        ...scheduledRunDraftFrom(fixtureScheduledRun()),
        [key]: value,
      }).some((issue) => issue.includes(expected as string)),
    ).toBe(true);
  });

  it("preserves immutable targets and annotations but discards server metadata and status", () => {
    const run = fixtureScheduledRun();
    run.resource.metadata = {
      ...run.resource.metadata,
      annotations: { owner: "team" },
      resourceVersion: "42",
    };
    const payload = scheduledRunPayloadFrom(
      {
        ...scheduledRunDraftFrom(run),
        agentTemplate: "another-agent",
        harness: "another-harness",
        prompt: "New prompt",
        suspended: true,
      },
      run,
    );
    expect(payload.resource.spec).toMatchObject({
      targetRef: run.resource.spec.targetRef,
      harnessRef: run.resource.spec.harnessRef,
      prompt: "New prompt",
      suspended: true,
    });
    expect(payload.resource.metadata).toMatchObject({
      annotations: { owner: "team" },
    });
    expect(payload.resource.metadata).not.toHaveProperty("resourceVersion");
    expect(payload.resource).not.toHaveProperty("status");
  });
});

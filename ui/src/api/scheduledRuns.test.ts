import { afterEach, describe, expect, it } from "vitest";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { ScheduledRunService } from "@/generated/kagent/api/v1alpha1/scheduled_runs_pb";
import { AgentInstanceService } from "@/generated/kagent/api/v1alpha1/agent_instances_pb";
import { setApiTransport } from "./transport";
import { apiClient } from "./client";
import { fixtureScheduledRun } from "@/mocks/scheduledRuns";

afterEach(() => setApiTransport(undefined));

describe("scheduled run gRPC operations", () => {
  it("writes only authored resource fields, including an explicit false", async () => {
    let seen: unknown;
    setApiTransport(
      createRouterTransport(({ service }) =>
        service(ScheduledRunService, {
          updateScheduledRun(request) {
            seen = request;
            return {
              scheduledRun: { ref: request.ref, resource: request.resource },
            };
          },
        }),
      ),
    );
    const run = fixtureScheduledRun();
    const dirtyResource = {
      ...run.resource,
      metadata: {
        ...run.resource.metadata,
        managedFields: [{ manager: "controller" }],
        resourceVersion: "42",
        uid: "uid",
        annotations: { owner: "team" },
      },
    };
    await apiClient.scheduledRuns.update({ ...run, resource: dirtyResource });
    expect(seen).toMatchObject({
      ref: { name: run.name, namespace: run.namespace },
      resource: {
        apiVersion: "kagent.dev/v1alpha3",
        kind: "ScheduledRun",
        value: {
          metadata: {
            name: run.name,
            namespace: run.namespace,
            annotations: { owner: "team" },
          },
          spec: { suspended: false, allowSessionInteraction: false },
        },
      },
    });
    const value = (seen as { resource: { value: Record<string, unknown> } })
      .resource.value;
    expect(value).not.toHaveProperty("status");
    expect(value.metadata).not.toHaveProperty("managedFields");
    expect(value.metadata).not.toHaveProperty("resourceVersion");
  });

  it("passes opaque pagination tokens and preserves conversation and task identities", async () => {
    const start = new Date("2026-09-07T09:00:00.123Z");
    let seen: unknown;
    setApiTransport(
      createRouterTransport(({ service }) =>
        service(ScheduledRunService, {
          listScheduledRunExecutions(request) {
            seen = request;
            return {
              executions: [
                {
                  id: "execution-1",
                  startTime: timestampFromDate(start),
                  trigger: "Manual",
                  status: "InProgress",
                  agentInstanceId: "instance-1",
                  taskId: "task-1",
                },
              ],
              page: { nextPageToken: "opaque-next" },
            };
          },
        }),
      ),
    );
    expect(
      await apiClient.scheduledRuns.executions(
        "kagent",
        "daily-report",
        "opaque-current",
      ),
    ).toMatchObject({
      executions: [
        {
          id: "execution-1",
          startTime: start.toISOString(),
          agentInstanceId: "instance-1",
          taskId: "task-1",
        },
      ],
      nextPageToken: "opaque-next",
    });
    expect(seen).toMatchObject({
      ref: { namespace: "kagent", name: "daily-report" },
      page: { limit: 50, pageToken: "opaque-current" },
    });
  });

  it("reports refused reads instead of an empty list", async () => {
    setApiTransport(
      createRouterTransport(({ service }) =>
        service(ScheduledRunService, {
          listScheduledRuns() {
            throw new ConnectError("No schedule access", Code.PermissionDenied);
          },
        }),
      ),
    );
    await expect(apiClient.scheduledRuns.list("kagent")).rejects.toThrow(
      "No schedule access",
    );
  });

  it("reads schedule conversation access from the server without changing ordinary instances", async () => {
    setApiTransport(
      createRouterTransport(({ service }) =>
        service(AgentInstanceService, {
          getAgentInstance(request) {
            return {
              agentInstance: {
                id: request.agentInstanceId,
                namespace: "kagent",
              },
              readOnly: request.agentInstanceId === "scheduled",
              scheduledRun: request.agentInstanceId === "scheduled",
            };
          },
        }),
      ),
    );
    expect(
      await apiClient.agentInstances.get("scheduled"),
    ).toMatchObject({ readOnly: true, scheduledRun: true });
    expect(
      await apiClient.agentInstances.get("ordinary"),
    ).not.toHaveProperty("readOnly");
  });
});

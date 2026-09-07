import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ThemeProvider } from "@emotion/react";
import { SWRConfig } from "swr";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import { createRouterTransport } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { ScheduledRunService } from "@/generated/kagent/api/v1alpha1/scheduled_runs_pb";
import { SystemService } from "@/generated/kagent/api/v1alpha1/system_pb";
import { AgentTemplateService } from "@/generated/kagent/api/v1alpha1/agent_templates_pb";
import { setApiTransport } from "@/api/transport";
import { themeFor } from "@/theme/theme";
import { fixtureScheduledRun } from "@/mocks/scheduledRuns";
import { paths } from "@/router/routes";
import { ScheduledRunsPage } from "./ScheduledRunsPage";
import { ScheduledRunNewPage } from "./ScheduledRunNewPage";
import { ScheduledRunDetailsPage } from "./ScheduledRunDetailsPage";
import { wrap } from "@/api/grpc/wire";
import type { ScheduledRunResource } from "@/api/domain/scheduledRuns";

afterEach(() => {
  cleanup();
  setApiTransport(undefined);
});

function backend() {
  let resource = fixtureScheduledRun().resource;
  const seen = {
    namespaces: [] as string[],
    writes: [] as ScheduledRunResource[],
    tokens: [] as string[],
    triggers: 0,
  };
  const message = () => ({
    ref: { namespace: "kagent", name: "daily-report" },
    resource: wrap("ScheduledRun", resource),
  });
  setApiTransport(
    createRouterTransport(({ service }) => {
      service(SystemService, {
        listNamespaces: () => ({
          namespaces: [{ name: "kagent" }, { name: "platform" }],
        }),
      });
      service(AgentTemplateService, {
        listAgentTemplates: () => ({ agentTemplates: [] }),
      });
      service(ScheduledRunService, {
        listScheduledRuns(request) {
          seen.namespaces.push(request.namespace);
          return {
            scheduledRuns: request.namespace === "kagent" ? [message()] : [],
          };
        },
        getScheduledRun: () => ({ scheduledRun: message() }),
        createScheduledRun(request) {
          resource = request.resource?.value as unknown as ScheduledRunResource;
          seen.writes.push(resource);
          return { scheduledRun: { ...message(), ref: request.ref } };
        },
        updateScheduledRun(request) {
          resource = request.resource?.value as unknown as ScheduledRunResource;
          seen.writes.push(resource);
          return { scheduledRun: message() };
        },
        listScheduledRunExecutions(request) {
          const token = request.page?.pageToken ?? "";
          seen.tokens.push(token);
          return {
            executions: [
              {
                id: token ? "old-execution" : "new-execution",
                agentInstanceId: token ? "older-instance" : "latest-instance",
                startTime: timestampFromDate(
                  new Date(
                    token ? "2026-09-06T09:00:00Z" : "2026-09-07T09:00:00Z",
                  ),
                ),
                trigger: "Scheduled",
                status: "Succeeded",
              },
            ],
            page: { nextPageToken: token ? "" : "opaque-next" },
          };
        },
        triggerScheduledRun() {
          seen.triggers++;
          return {
            execution: {
              id: "queued",
              status: "InProgress",
              trigger: "Manual",
              startTime: timestampFromDate(new Date()),
            },
          };
        },
      });
    }),
  );
  return seen;
}

function show(entry: string) {
  return render(
    <ThemeProvider theme={themeFor("dark")}>
      <SWRConfig
        value={{
          provider: () => new Map(),
          dedupingInterval: 0,
          shouldRetryOnError: false,
        }}
      >
        <MemoryRouter initialEntries={[entry]}>
          <Routes>
            <Route path={paths.schedules} element={<ScheduledRunsPage />} />
            <Route path={paths.scheduleNew} element={<ScheduledRunNewPage />} />
            <Route
              path={paths.scheduleDetail}
              element={<ScheduledRunDetailsPage />}
            />
          </Routes>
        </MemoryRouter>
      </SWRConfig>
    </ThemeProvider>,
  );
}

describe("schedule pages through the gRPC client", () => {
  it("lists known namespaces explicitly and links to schedule details", async () => {
    const seen = backend();
    show("/schedules");
    const link = await screen.findByRole("link", { name: "daily-report" });
    expect(link).toHaveAttribute("href", "/schedules/kagent/daily-report");
    expect(seen.namespaces.sort()).toEqual(["kagent", "platform"]);
    await userEvent.click(link);
    expect(await screen.findByText("Execution history")).toBeInTheDocument();
  });

  it("creates a complete schedule with read-only conversations by default", async () => {
    const seen = backend();
    show(
      "/schedules/new?namespace=kagent&agentTemplate=k8s-agent-7f3a91c&harness=k8s-agent",
    );
    const user = userEvent.setup();
    expect(
      screen.getByRole("button", { name: "Create schedule" }),
    ).toBeDisabled();
    await user.type(
      screen.getByRole("textbox", { name: "Name" }),
      "daily-report",
    );
    await user.type(
      screen.getByRole("textbox", { name: "Schedule" }),
      "0 9 * * *",
    );
    await user.type(
      screen.getByRole("textbox", { name: "Prompt" }),
      "Report cluster health",
    );
    await user.click(screen.getByRole("button", { name: "Create schedule" }));
    await waitFor(() => expect(seen.writes).toHaveLength(1));
    expect(seen.writes[0].spec).toMatchObject({
      targetRef: { kind: "AgentTemplate", name: "k8s-agent-7f3a91c" },
      harnessRef: { name: "k8s-agent" },
      allowSessionInteraction: false,
      executionTimeout: "15m",
      prompt: "Report cluster health",
    });
    expect(await screen.findByText("Execution history")).toBeInTheDocument();
  });

  it("pauses, edits, triggers and reads older executions without losing conversation links", async () => {
    const seen = backend();
    show("/schedules/kagent/daily-report");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Suspend" }));
    await waitFor(() => expect(seen.writes[0]?.spec.suspended).toBe(true));
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByRole("textbox", { name: "Name" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Agent" })).toBeDisabled();
    const prompt = screen.getByRole("textbox", { name: "Prompt" });
    await user.clear(prompt);
    await user.type(prompt, "Changed report");
    await user.click(
      screen.getByRole("switch", { name: "Allow conversation interaction" }),
    );
    await user.click(screen.getByRole("button", { name: "Save schedule" }));
    await waitFor(() =>
      expect(seen.writes.at(-1)?.spec).toMatchObject({
        prompt: "Changed report",
        suspended: true,
        allowSessionInteraction: true,
      }),
    );
    await user.click(
      await screen.findByRole("button", { name: "Trigger now" }),
    );
    await waitFor(() => expect(seen.triggers).toBe(1));
    await user.click(
      screen.getByRole("button", { name: "Load older executions" }),
    );
    await waitFor(() =>
      expect(
        screen.getAllByRole("link", { name: "Open conversation" }),
      ).toHaveLength(2),
    );
    expect(seen.tokens).toContain("opaque-next");
    expect(
      screen
        .getAllByRole("link", { name: "Open conversation" })
        .map((link) => link.getAttribute("href")),
    ).toEqual([
      "/agents/kagent/latest-instance/chat",
      "/agents/kagent/older-instance/chat",
    ]);
  });
});

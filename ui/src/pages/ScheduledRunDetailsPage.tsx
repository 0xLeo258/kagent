import { useState } from "react";
import { Alert, Button, Card, Descriptions, Skeleton, Space, Tag } from "antd";
import { Pause, Pencil, Play } from "lucide-react";
import toast from "react-hot-toast";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  apiClient,
  scheduledRunStatus,
  scheduledRunWrite,
  useScheduledRun,
  useScheduledRunExecutions,
} from "@/api";
import { PageFrame } from "@/components/Structure/PageFrame";
import { DeleteResourceButton } from "@/components/table/DeleteResourceButton";
import { RefreshButton } from "@/components/table/RefreshButton";
import { ScheduledRunForm } from "@/components/schedules/ScheduledRunForm";
import {
  scheduledRunDraftFrom,
  scheduledRunPayloadFrom,
  type ScheduledRunDraft,
} from "@/components/schedules/scheduledRunDraft";
import { ExecutionHistoryTable } from "@/components/schedules/ExecutionHistoryTable";
import { executionDate } from "@/components/schedules/scheduledRunDisplay";
import { buildPath, paths } from "@/router/routes";

export function ScheduledRunDetailsPage() {
  const { namespace, name } = useParams();
  // Local edits and pagination belong to one schedule, never the previous route.
  return (
    <ScheduledRunDetails
      key={`${namespace}/${name}`}
      namespace={namespace ?? ""}
      name={name ?? ""}
    />
  );
}

function ScheduledRunDetails({
  namespace,
  name,
}: {
  namespace: string;
  name: string;
}) {
  const navigate = useNavigate();
  const run = useScheduledRun(namespace, name);
  const history = useScheduledRunExecutions(namespace, name);
  const [draft, setDraft] = useState<ScheduledRunDraft>();
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState<string>();
  const data = run.data;
  async function refresh() {
    await Promise.all([run.refresh(), history.refresh()]);
  }
  async function perform(
    action: string,
    operation: () => Promise<unknown>,
    success: string,
  ) {
    if (busy) return;
    setBusy(action);
    setError(undefined);
    try {
      await operation();
      await refresh();
      toast.success(success);
      if (action === "save") setDraft(undefined);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(undefined);
    }
  }
  const status = data && scheduledRunStatus(data);
  return (
    <PageFrame
      title={name}
      description={`Schedule in ${namespace}`}
      actions={
        <Space wrap>
          <Link to={paths.schedules}>
            <Button>Back to schedules</Button>
          </Link>
          <RefreshButton
            what="Schedule"
            onRefresh={refresh}
            loading={run.isValidating}
          />
          {data && !draft && (
            <>
              <Button
                icon={<Pencil size={14} />}
                onClick={() => setDraft(scheduledRunDraftFrom(data))}
                disabled={Boolean(busy)}
              >
                Edit
              </Button>
              <Button
                icon={
                  data.resource.spec.suspended ? (
                    <Play size={14} />
                  ) : (
                    <Pause size={14} />
                  )
                }
                loading={busy === "suspend"}
                disabled={Boolean(busy)}
                onClick={() =>
                  void perform(
                    "suspend",
                    () =>
                      apiClient.scheduledRuns.update(
                        scheduledRunWrite(data, {
                          ...data.resource.spec,
                          suspended: !data.resource.spec.suspended,
                        }),
                      ),
                    data.resource.spec.suspended
                      ? "Schedule resumed"
                      : "Schedule suspended",
                  )
                }
              >
                {data.resource.spec.suspended ? "Resume" : "Suspend"}
              </Button>
              <Button
                icon={<Play size={14} />}
                loading={busy === "trigger"}
                disabled={Boolean(busy)}
                onClick={() =>
                  void perform(
                    "trigger",
                    () => apiClient.scheduledRuns.trigger(namespace, name),
                    "Execution queued",
                  )
                }
              >
                Trigger now
              </Button>
              <DeleteResourceButton
                kind="schedule"
                name={name}
                onDelete={() => apiClient.scheduledRuns.remove(namespace, name)}
                onDeleted={() => navigate(paths.schedules)}
                disabled={Boolean(busy)}
              />
            </>
          )}
        </Space>
      }
    >
      <Space orientation="vertical" size="large" css={{ display: "flex" }}>
        {run.error && (
          <Alert
            type="error"
            showIcon
            title="Could not load this schedule"
            description={run.error.message}
          />
        )}
        {error && (
          <Alert
            type="error"
            showIcon
            title="Schedule action failed"
            description={error}
          />
        )}
        {run.isLoading && <Skeleton active />}
        {data &&
          (draft ? (
            <ScheduledRunForm
              draft={draft}
              onChange={setDraft}
              editing
              submitting={Boolean(busy)}
              onCancel={() => setDraft(undefined)}
              onSubmit={() =>
                void perform(
                  "save",
                  () =>
                    apiClient.scheduledRuns.update(
                      scheduledRunPayloadFrom(draft, data),
                    ),
                  "Schedule saved",
                )
              }
            />
          ) : (
            <Card
              title={
                <Space>
                  Schedule details
                  <Tag color={status?.color}>{status?.label}</Tag>
                </Space>
              }
            >
              {status?.message && (
                <Alert type="warning" title={status.message} />
              )}
              <Descriptions
                column={{ xs: 1, sm: 2, lg: 3 }}
                items={[
                  {
                    key: "agent",
                    label: "Agent",
                    children: (
                      <Link
                        to={buildPath(paths.agent, {
                          namespace,
                          agentTemplate: data.resource.spec.targetRef.name,
                          harness: data.resource.spec.harnessRef.name,
                        })}
                      >
                        {data.resource.spec.targetRef.name} on{" "}
                        {data.resource.spec.harnessRef.name}
                      </Link>
                    ),
                  },
                  {
                    key: "schedule",
                    label: "Schedule",
                    children: data.resource.spec.schedule,
                  },
                  {
                    key: "zone",
                    label: "Time zone",
                    children: data.resource.spec.timeZone ?? "UTC",
                  },
                  {
                    key: "interaction",
                    label: "Conversation interaction",
                    children: data.resource.spec.allowSessionInteraction
                      ? "Allowed"
                      : "Read-only",
                  },
                  {
                    key: "timeout",
                    label: "Execution timeout",
                    children: data.resource.spec.executionTimeout ?? "15m",
                  },
                  {
                    key: "last",
                    label: "Last execution",
                    children: executionDate(
                      data.resource.status?.lastExecutionTime,
                    ),
                  },
                  {
                    key: "next",
                    label: "Next execution",
                    children: executionDate(
                      data.resource.status?.nextExecutionTime,
                    ),
                  },
                ]}
              />
              <div
                css={{
                  whiteSpace: "pre-wrap",
                  overflowWrap: "anywhere",
                  marginTop: 20,
                }}
              >
                <strong>Prompt</strong>
                <p>{data.resource.spec.prompt}</p>
              </div>
            </Card>
          ))}
        <Card title="Execution history">
          {history.error && (
            <Alert
              type="error"
              showIcon
              title="Could not load execution history"
              description={history.error.message}
            />
          )}
          <ExecutionHistoryTable
            executions={history.executions}
            loading={history.isLoading}
          />
          {history.hasMore && (
            <Button
              loading={history.isValidating}
              onClick={() => void history.loadMore()}
            >
              Load older executions
            </Button>
          )}
        </Card>
      </Space>
    </PageFrame>
  );
}

import { Alert, Button, Select, Space, Table, Tag, Tooltip } from "antd";
import { Plus } from "lucide-react";
import { Link, useSearchParams } from "react-router-dom";
import {
  apiClient,
  scheduledRunStatus,
  useNamespaces,
  useScheduledRuns,
  type ScheduledRun,
} from "@/api";
import { PageFrame } from "@/components/Structure/PageFrame";
import { DeleteResourceButton } from "@/components/table/DeleteResourceButton";
import { RefreshButton } from "@/components/table/RefreshButton";
import { executionDate } from "@/components/schedules/scheduledRunDisplay";
import { buildPath, paths } from "@/router/routes";

export function ScheduledRunsPage() {
  const [params, setParams] = useSearchParams();
  const namespace = params.get("namespace") ?? undefined;
  const runs = useScheduledRuns(namespace);
  const namespaces = useNamespaces();
  const agentTemplate = params.get("agentTemplate");
  const harness = params.get("harness");
  const filtered = (runs.data ?? []).filter(
    (run) =>
      (!agentTemplate || run.resource.spec.targetRef.name === agentTemplate) &&
      (!harness || run.resource.spec.harnessRef.name === harness),
  );
  return (
    <PageFrame
      title="Schedules"
      description="Recurring agent prompts, each with its own conversation and execution history."
      actions={
        <Space>
          <RefreshButton
            onRefresh={runs.refresh}
            what="Schedules"
            loading={runs.isValidating}
          />
          <Link
            to={`${paths.scheduleNew}${params.size ? `?${params.toString()}` : ""}`}
          >
            <Button type="primary" icon={<Plus size={14} />}>
              New schedule
            </Button>
          </Link>
        </Space>
      }
    >
      <Select
        aria-label="Filter namespace"
        allowClear
        placeholder="All namespaces"
        value={namespace}
        onChange={(value?: string) =>
          setParams(value ? { namespace: value } : {})
        }
        options={(namespaces.data ?? []).map((entry) => ({
          value: entry.name,
        }))}
        css={{ width: 220, marginBottom: 16 }}
      />
      {(runs.error || namespaces.error) && (
        <Alert
          type="error"
          showIcon
          title="Could not load schedules"
          description={(runs.error ?? namespaces.error)?.message}
        />
      )}
      {agentTemplate && (
        <Alert
          type="info"
          title={`Schedules for ${agentTemplate}${harness ? ` on ${harness}` : ""}`}
          action={
            <Button onClick={() => setParams(namespace ? { namespace } : {})}>
              Show all agents
            </Button>
          }
        />
      )}
      <Table<ScheduledRun>
        rowKey={(run) => `${run.namespace}/${run.name}`}
        data-testid="schedules-table"
        dataSource={filtered}
        loading={runs.isLoading}
        scroll={{ x: 850 }}
        locale={{
          emptyText: runs.error
            ? "Schedules are unavailable"
            : "No schedules yet",
        }}
        columns={[
          {
            title: "Name",
            render: (_, run) => (
              <Link
                to={buildPath(paths.scheduleDetail, {
                  namespace: run.namespace,
                  name: run.name,
                })}
              >
                {run.name}
              </Link>
            ),
          },
          { title: "Namespace", dataIndex: "namespace" },
          {
            title: "Agent",
            render: (_, run) => (
              <Link
                to={buildPath(paths.agent, {
                  namespace: run.namespace,
                  agentTemplate: run.resource.spec.targetRef.name,
                  harness: run.resource.spec.harnessRef.name,
                })}
              >
                {run.resource.spec.targetRef.name} on{" "}
                {run.resource.spec.harnessRef.name}
              </Link>
            ),
          },
          {
            title: "Schedule",
            render: (_, run) => (
              <span>
                {run.resource.spec.schedule} (
                {run.resource.spec.timeZone ?? "UTC"})
              </span>
            ),
          },
          {
            title: "Status",
            render: (_, run) => {
              const status = scheduledRunStatus(run);
              return (
                <Tooltip title={status.message}>
                  <Tag color={status.color}>{status.label}</Tag>
                </Tooltip>
              );
            },
          },
          {
            title: "Last execution",
            render: (_, run) =>
              executionDate(run.resource.status?.lastExecutionTime),
          },
          {
            title: "Next execution",
            render: (_, run) =>
              executionDate(run.resource.status?.nextExecutionTime),
          },
          {
            title: "",
            render: (_, run) => (
              <DeleteResourceButton
                kind="schedule"
                name={run.name}
                onDelete={() =>
                  apiClient.scheduledRuns.remove(run.namespace, run.name)
                }
                onDeleted={runs.refresh}
              />
            ),
          },
        ]}
      />
    </PageFrame>
  );
}

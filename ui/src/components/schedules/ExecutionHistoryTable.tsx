import { useEffect, useState } from "react";
import { Table, Tag, Typography } from "antd";
import { Link } from "react-router-dom";
import type { ScheduledRunExecution } from "@/api";
import { buildPath, paths } from "@/router/routes";
import { executionDate } from "./scheduledRunDisplay";

function executionDuration(
  execution: ScheduledRunExecution,
  now: number,
): string {
  if (!execution.completionTime && execution.status !== "InProgress")
    return "—";
  const seconds = Math.floor(
    ((execution.completionTime
      ? new Date(execution.completionTime).getTime()
      : now) -
      new Date(execution.startTime).getTime()) /
      1000,
  );
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  return seconds < 60
    ? `${seconds}s`
    : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export function ExecutionHistoryTable({
  executions,
  loading,
}: {
  executions: ScheduledRunExecution[];
  loading?: boolean;
}) {
  const [now, setNow] = useState(() => Date.now());
  const running = executions.some(
    (execution) => execution.status === "InProgress",
  );
  useEffect(() => {
    if (!running) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [running]);
  return (
    <Table<ScheduledRunExecution>
      rowKey="id"
      data-testid="schedule-executions"
      dataSource={executions}
      loading={loading}
      pagination={false}
      scroll={{ x: 950 }}
      locale={{ emptyText: "No executions yet" }}
      columns={[
        { title: "Started", dataIndex: "startTime", render: executionDate },
        {
          title: "Completed",
          dataIndex: "completionTime",
          render: executionDate,
        },
        {
          title: "Duration",
          render: (_, execution) => executionDuration(execution, now),
        },
        { title: "Trigger", dataIndex: "trigger" },
        {
          title: "Status",
          dataIndex: "status",
          render: (status: string) => (
            <Tag
              color={
                status === "Succeeded"
                  ? "success"
                  : status === "InProgress"
                    ? "processing"
                    : "error"
              }
            >
              {status === "InProgress" ? "Running" : status}
            </Tag>
          ),
        },
        {
          title: "Conversation",
          render: (_, execution) =>
            execution.agentInstanceId ? (
              <Link
                to={buildPath(paths.agentChat, {
                  id: execution.agentInstanceId,
                })}
              >
                Open conversation
              </Link>
            ) : (
              "—"
            ),
        },
        {
          title: "Details",
          dataIndex: "statusMessage",
          render: (value: string | undefined) => (
            <Typography.Text>{value || "—"}</Typography.Text>
          ),
        },
      ]}
    />
  );
}

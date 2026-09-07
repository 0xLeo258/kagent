import { useState } from "react";
import { Alert } from "antd";
import toast from "react-hot-toast";
import { useNavigate, useSearchParams } from "react-router-dom";
import { apiClient } from "@/api";
import { PageFrame } from "@/components/Structure/PageFrame";
import { ScheduledRunForm } from "@/components/schedules/ScheduledRunForm";
import {
  emptyScheduledRunDraft,
  scheduledRunPayloadFrom,
} from "@/components/schedules/scheduledRunDraft";
import { buildPath, paths } from "@/router/routes";

export function ScheduledRunNewPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [draft, setDraft] = useState(() => ({
    ...emptyScheduledRunDraft(params.get("namespace") ?? undefined),
    agentTemplate: params.get("agentTemplate") ?? "",
    harness: params.get("harness") ?? "",
  }));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  async function create() {
    setSubmitting(true);
    setError(undefined);
    try {
      const run = await apiClient.scheduledRuns.create(
        scheduledRunPayloadFrom(draft),
      );
      toast.success(`Schedule ${run.name} created`);
      navigate(
        buildPath(paths.scheduleDetail, {
          namespace: run.namespace,
          name: run.name,
        }),
      );
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSubmitting(false);
    }
  }
  return (
    <PageFrame
      title="New schedule"
      description="Send a recurring prompt to an agent. Each execution has its own conversation."
    >
      {error && (
        <Alert
          type="error"
          showIcon
          title="Could not create the schedule"
          description={error}
        />
      )}
      <ScheduledRunForm
        draft={draft}
        onChange={setDraft}
        onSubmit={() => void create()}
        onCancel={() => navigate(paths.schedules)}
        submitting={submitting}
      />
    </PageFrame>
  );
}

import {
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
} from "antd";
import { agentPairsFrom, useAgentTemplates, useNamespaces } from "@/api";
import {
  scheduledRunDraftIssues,
  type ScheduledRunDraft,
} from "./scheduledRunDraft";

export function ScheduledRunForm({
  draft,
  onChange,
  onSubmit,
  onCancel,
  editing = false,
  submitting = false,
}: {
  draft: ScheduledRunDraft;
  onChange: (draft: ScheduledRunDraft) => void;
  onSubmit: () => void;
  onCancel: () => void;
  editing?: boolean;
  submitting?: boolean;
}) {
  const namespaces = useNamespaces();
  const templates = useAgentTemplates(draft.namespace);
  const pairs = agentPairsFrom(templates.data ?? []);
  const issues = scheduledRunDraftIssues(draft);
  const update = <K extends keyof ScheduledRunDraft>(
    key: K,
    value: ScheduledRunDraft[K],
  ) => onChange({ ...draft, [key]: value });
  return (
    <Form
      layout="vertical"
      onFinish={onSubmit}
      disabled={submitting}
      css={{ maxWidth: 800 }}
    >
      {(namespaces.error || templates.error) && (
        <Alert
          type="error"
          showIcon
          title="Could not load agent choices"
          description={(namespaces.error ?? templates.error)?.message}
        />
      )}
      <Form.Item label="Name" required>
        <Input
          aria-label="Name"
          value={draft.name}
          disabled={editing || submitting}
          onChange={(event) => update("name", event.target.value)}
          placeholder="daily-report"
        />
      </Form.Item>
      <Form.Item label="Namespace" required>
        <Select
          aria-label="Namespace"
          value={draft.namespace}
          disabled={editing || submitting}
          loading={namespaces.isLoading}
          onChange={(namespace: string) =>
            onChange({ ...draft, namespace, agentTemplate: "", harness: "" })
          }
          options={(namespaces.data ?? []).map((entry) => ({
            value: entry.name,
            label: entry.name,
          }))}
        />
      </Form.Item>
      <Form.Item
        label="Agent"
        required
        help={
          editing
            ? "The template and harness are fixed for this schedule."
            : "Each execution starts a new conversation with this agent."
        }
      >
        <Select
          aria-label="Agent"
          showSearch
          optionFilterProp="label"
          loading={templates.isLoading}
          disabled={editing || submitting}
          value={
            draft.agentTemplate && draft.harness
              ? `${draft.agentTemplate}/${draft.harness}`
              : undefined
          }
          placeholder="Select an agent"
          onChange={(value: string) => {
            const [agentTemplate, harness] = value.split("/");
            onChange({ ...draft, agentTemplate, harness });
          }}
          options={
            editing
              ? [
                  {
                    value: `${draft.agentTemplate}/${draft.harness}`,
                    label: `${draft.agentTemplate} on ${draft.harness}`,
                  },
                ]
              : pairs.map((pair) => ({
                  value: `${pair.agentTemplate}/${pair.harness}`,
                  label: `${pair.agentTemplate} on ${pair.harness}`,
                }))
          }
        />
      </Form.Item>
      <Form.Item
        label="Schedule"
        required
        help="Five fields: minute hour day month weekday. For example, 0 9 * * 1-5 runs on weekdays at 09:00."
      >
        <Input
          aria-label="Schedule"
          value={draft.schedule}
          onChange={(event) => update("schedule", event.target.value)}
          placeholder="0 9 * * 1-5"
        />
      </Form.Item>
      <Form.Item label="Time zone" help="IANA time zone. Leave blank for UTC.">
        <Input
          aria-label="Time zone"
          value={draft.timeZone}
          onChange={(event) => update("timeZone", event.target.value)}
          placeholder="UTC"
        />
      </Form.Item>
      <Form.Item label="Prompt" required>
        <Input.TextArea
          aria-label="Prompt"
          value={draft.prompt}
          onChange={(event) => update("prompt", event.target.value)}
          rows={6}
        />
      </Form.Item>
      <Form.Item
        label="Suspend automatic executions"
        help="Manual triggers remain available."
      >
        <Switch
          aria-label="Suspend automatic executions"
          checked={draft.suspended}
          onChange={(value) => update("suspended", value)}
        />
      </Form.Item>
      {!editing && (
        <Alert
          type="info"
          showIcon
          title="Conversation access"
          description="Creating this schedule binds it to your account. Only you can reply to its conversations; other authorized readers have read-only access."
          css={{ marginBottom: 16 }}
        />
      )}
      <Form.Item
        label="Execution timeout"
        required
        help="Includes starting the conversation and waiting for the result."
      >
        <Input
          aria-label="Execution timeout"
          value={draft.executionTimeout}
          onChange={(event) => update("executionTimeout", event.target.value)}
        />
      </Form.Item>
      <Form.Item
        label="Recent completed executions"
        help="1–100. Older executions remain in execution history."
      >
        <InputNumber
          aria-label="Recent completed executions"
          min={1}
          max={100}
          value={draft.recentExecutionsLimit}
          onChange={(value) => update("recentExecutionsLimit", value ?? 0)}
        />
      </Form.Item>
      {issues.length > 0 && (
        <Alert
          type="info"
          title="Complete the schedule"
          description={
            <ul>
              {issues.map((issue) => (
                <li key={issue}>{issue}</li>
              ))}
            </ul>
          }
          css={{ marginBottom: 16 }}
        />
      )}
      <Space>
        <Button
          type="primary"
          htmlType="submit"
          loading={submitting}
          disabled={issues.length > 0}
        >
          {editing ? "Save schedule" : "Create schedule"}
        </Button>
        <Button onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
      </Space>
    </Form>
  );
}

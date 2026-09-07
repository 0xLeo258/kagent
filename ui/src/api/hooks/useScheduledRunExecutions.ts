import useSWRInfinite from "swr/infinite";
import { apiClient } from "../client";
import type { ApiError } from "../ApiError";
import type { ScheduledRunExecutionPage } from "../domain/scheduledRuns";

export function useScheduledRunExecutions(namespace?: string, name?: string) {
  const { data, error, isLoading, isValidating, setSize, size, mutate } =
    useSWRInfinite<ScheduledRunExecutionPage, ApiError>(
      (index, previous: ScheduledRunExecutionPage | null) => {
        if (!namespace || !name || (index > 0 && !previous?.nextPageToken))
          return null;
        return [
          "scheduledRuns.executions",
          namespace,
          name,
          index === 0 ? "" : previous?.nextPageToken,
        ];
      },
      ([, ns, run, token]: [string, string, string, string]) =>
        apiClient.scheduledRuns.executions(ns, run, token || undefined),
      { refreshInterval: 5000, revalidateAll: true },
    );
  const byId = new Map(
    (data ?? [])
      .flatMap((page) => page.executions)
      .map((execution) => [execution.id, execution]),
  );
  return {
    executions: [...byId.values()],
    error,
    isLoading,
    isValidating,
    hasMore: Boolean(data?.at(-1)?.nextPageToken),
    loadMore: () => setSize(size + 1),
    refresh: async () => {
      if (!namespace || !name) return;
      const refreshed: ScheduledRunExecutionPage[] = [];
      let pageToken: string | undefined;
      // New executions can shift page boundaries. Follow the freshly returned
      // cursors so refreshing several pages cannot skip a boundary execution.
      for (let index = 0; index < Math.max(data?.length ?? 0, 1); index++) {
        const page = await apiClient.scheduledRuns.executions(
          namespace,
          name,
          pageToken,
        );
        refreshed.push(page);
        pageToken = page.nextPageToken;
        if (!pageToken) break;
      }
      await mutate(refreshed, { revalidate: false });
    },
  };
}

import { apiClient } from "../client";
import { useApiResource } from "./useApiResource";
import { sortedByFields } from "../order";

export function useScheduledRuns(namespace?: string) {
  return useApiResource(
    ["scheduledRuns.list", namespace ?? "*"],
    async () => {
      const scope = namespace
        ? [namespace]
        : (await apiClient.namespaces.list()).map((entry) => entry.name);
      // A refused namespace is an incomplete list, not an empty namespace.
      return sortedByFields(
        (
          await Promise.all(
            scope.map((entry) => apiClient.scheduledRuns.list(entry)),
          )
        ).flat(),
      );
    },
    {
      refreshInterval: 5000,
    },
  );
}

export function useScheduledRun(namespace?: string, name?: string) {
  return useApiResource(
    namespace && name ? ["scheduledRuns.get", namespace, name] : null,
    () => apiClient.scheduledRuns.get(namespace ?? "", name ?? ""),
    { refreshInterval: 5000 },
  );
}

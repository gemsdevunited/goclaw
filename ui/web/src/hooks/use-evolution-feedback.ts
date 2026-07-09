import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useHttp } from "@/hooks/use-ws";
import { queryKeys } from "@/lib/query-keys";
import type { EvolutionMetric, FeedbackValue } from "@/types/evolution";

export function useEvolutionFeedback(agentId: string, timeRange: string) {
  const http = useHttp();

  const since = useMemo(() => {
    const d = new Date();
    d.setDate(d.getDate() - (timeRange === "90d" ? 90 : timeRange === "30d" ? 30 : 7));
    d.setHours(0, 0, 0, 0);
    return d.toISOString();
  }, [timeRange]);

  const { data, isLoading, refetch } = useQuery({
    queryKey: queryKeys.evolution.metrics(agentId, { type: "feedback", timeRange }),
    queryFn: () =>
      http.get<EvolutionMetric<FeedbackValue>[]>(`/v1/agents/${agentId}/evolution/metrics`, {
        type: "feedback",
        since,
        limit: "100",
      }),
  });

  return {
    feedback: data ?? [],
    loading: isLoading,
    refetch,
  };
}

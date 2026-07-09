import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { MessageSquare, ExternalLink, ThumbsUp, ThumbsDown } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { formatRelativeTime } from "@/lib/format";
import type { EvolutionMetric, FeedbackValue } from "@/types/evolution";

interface Props {
  feedback: EvolutionMetric<FeedbackValue>[];
  loading: boolean;
}

export function EvolutionFeedbackTable({ feedback, loading }: Props) {
  const { t } = useTranslation("agents");

  if (loading) {
    return <div className="h-[120px] animate-pulse rounded-md bg-muted" />;
  }

  if (feedback.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-6 text-center text-muted-foreground border rounded-md">
        <MessageSquare className="h-8 w-8 text-muted-foreground/30 mb-2" />
        <p className="text-xs">{t("detail.evolution.noFeedback") || "No user feedback recorded for this period."}</p>
      </div>
    );
  }

  return (
    <div className="rounded-md border">
      <div className="overflow-x-auto">
        <table className="w-full text-sm min-w-[600px]">
          <thead>
            <tr className="border-b bg-muted/50 text-left">
              <th className="px-3 py-2 font-medium w-[100px]">Rating</th>
              <th className="px-3 py-2 font-medium w-[180px]">Issue Tags</th>
              <th className="px-3 py-2 font-medium">User Comment</th>
              <th className="px-3 py-2 font-medium w-[150px]">Created At</th>
              <th className="px-3 py-2 font-medium text-right w-[100px]">Session</th>
            </tr>
          </thead>
          <tbody>
            {feedback.map((f) => {
              const rating = f.value?.rating;
              const tags = f.value?.tags ?? [];
              const comment = f.value?.comment ?? "";

              return (
                <tr key={f.id} className="border-b hover:bg-muted/30">
                  <td className="px-3 py-2">
                    {rating === "good" ? (
                      <span className="flex items-center gap-1 text-green-600 text-xs font-semibold">
                        <ThumbsUp className="h-3.5 w-3.5" />
                        Good
                      </span>
                    ) : (
                      <span className="flex items-center gap-1 text-red-600 text-xs font-semibold">
                        <ThumbsDown className="h-3.5 w-3.5" />
                        Bad
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex flex-wrap gap-1">
                      {tags.length > 0 ? (
                        tags.map((t) => (
                          <Badge
                            key={t}
                            variant="outline"
                            className="text-[10px] px-1 py-0 bg-secondary/50 whitespace-nowrap"
                          >
                            {t}
                          </Badge>
                        ))
                      ) : (
                        <span className="text-xs text-muted-foreground">-</span>
                      )}
                    </div>
                  </td>
                  <td className="px-3 py-2 max-w-[300px]">
                    <p className="text-xs text-foreground font-medium break-words">
                      {comment || <span className="text-muted-foreground italic">No comment provided</span>}
                    </p>
                  </td>
                  <td className="px-3 py-2 text-xs text-muted-foreground whitespace-nowrap">
                    {formatRelativeTime(f.created_at)}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <Link
                      to={`/sessions/${f.session_key}`}
                      className="inline-flex items-center gap-1 text-xs text-primary hover:underline font-medium"
                      title="View chat session history"
                    >
                      <span>Trace</span>
                      <ExternalLink className="h-3 w-3" />
                    </Link>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

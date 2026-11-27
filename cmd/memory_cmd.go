package cmd

import (
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

// goclaw memory — agent memory documents (pgvector), via /v1/agents/{id}/memory/*.

func memoryCmd() *cobra.Command {
	return dataCmdGroup("memory", "Manage agent memory documents — list, get, search, set, delete, index",
		memoryListCmd(), memoryGetCmd(), memoryChunksCmd(), memorySearchCmd(),
		memorySetCmd(), memoryDeleteCmd(), memoryIndexCmd(), memoryIndexAllCmd(),
	)
}

func memoryDocPath(base, docPath string) string {
	return base + "/memory/documents/" + escapePathSegments(docPath)
}

func memoryListCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "list <agent>",
		Short: "List memory documents (all users unless --user)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodGet, withQuery(base+"/memory/documents", "user_id", user), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "only documents of this user ID")
	return cmd
}

func memoryGetCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "get <agent> <path>",
		Short: "Show a memory document with content",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodGet, withQuery(memoryDocPath(base, args[1]), "user_id", user), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID (empty = agent-level document)")
	return cmd
}

func memoryChunksCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "chunks <agent> <path>",
		Short: "List indexed chunks of a memory document",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodGet, withQuery(base+"/memory/chunks", "path", args[1], "user_id", user), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	return cmd
}

func memorySearchCmd() *cobra.Command {
	var user string
	var maxResults int
	var minScore float64
	cmd := &cobra.Command{
		Use:   "search <agent> <query>",
		Short: "Semantic search over an agent's memory",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/memory/search", map[string]any{
				"query": args[1], "user_id": user, "max_results": maxResults, "min_score": minScore,
			}, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	cmd.Flags().IntVar(&maxResults, "max-results", 10, "maximum results")
	cmd.Flags().Float64Var(&minScore, "min-score", 0, "minimum similarity score")
	return cmd
}

func memorySetCmd() *cobra.Command {
	var user, fromFile string
	var allowEmpty bool
	cmd := &cobra.Command{
		Use:   "set <agent> <path>",
		Short: "Create or replace a memory document (content from --from-file or stdin)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := readAgentFileContent(fromFile, os.Stdin, allowEmpty)
			if err != nil {
				return err
			}
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPut, memoryDocPath(base, args[1]),
				map[string]any{"content": content, "user_id": user}, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID (empty = agent-level document)")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read content from file (default: piped stdin)")
	cmd.Flags().BoolVar(&allowEmpty, "allow-empty", false, "allow writing an empty document")
	return cmd
}

func memoryDeleteCmd() *cobra.Command {
	var user string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <agent> <path>",
		Short: "Delete a memory document and its chunks (requires --force)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForce(force, "memory delete "+args[1]); err != nil {
				return err
			}
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodDelete, withQuery(memoryDocPath(base, args[1]), "user_id", user), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

func memoryIndexCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "index <agent> <path>",
		Short: "Re-chunk and re-embed one memory document",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/memory/index",
				map[string]any{"path": args[1], "user_id": user}, slowDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	return cmd
}

func memoryIndexAllCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "index-all <agent>",
		Short: "Re-index all memory documents of an agent (slow)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/memory/index-all",
				map[string]any{"user_id": user}, slowDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID (default: caller)")
	return cmd
}

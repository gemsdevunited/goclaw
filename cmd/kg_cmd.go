package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// goclaw kg — knowledge graph entities, traversal and dedup, via /v1/agents/{id}/kg/*.

func kgCmd() *cobra.Command {
	return dataCmdGroup("kg", "Manage an agent's knowledge graph — entities, graph, traverse, dedup, merge",
		kgStatsCmd(), kgEntitiesCmd(), kgGetCmd(), kgGraphCmd(), kgTraverseCmd(),
		kgUpsertCmd(), kgDeleteCmd(), kgMergeCmd(), kgExtractCmd(), kgDedupCmd(),
	)
}

func kgStatsCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "stats <agent>",
		Short: "Entity/relation counts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodGet, withQuery(base+"/kg/stats", "user_id", user), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	return cmd
}

func kgEntitiesCmd() *cobra.Command {
	var user, entityType, query string
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "entities <agent>",
		Short: "List or search entities",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			path := withQuery(base+"/kg/entities",
				"user_id", user, "type", entityType, "q", query,
				"limit", positiveIntString(limit), "offset", positiveIntString(offset))
			raw, err := gatewayJSON(http.MethodGet, path, nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	cmd.Flags().StringVar(&entityType, "type", "", "filter by entity type")
	cmd.Flags().StringVarP(&query, "query", "q", "", "search text")
	cmd.Flags().IntVar(&limit, "limit", 50, "page size")
	cmd.Flags().IntVar(&offset, "offset", 0, "page offset")
	return cmd
}

func kgGetCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "get <agent> <entity-id>",
		Short: "Show an entity with its relations",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodGet,
				withQuery(base+"/kg/entities/"+url.PathEscape(args[1]), "user_id", user), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	return cmd
}

func kgGraphCmd() *cobra.Command {
	var user string
	var limit int
	cmd := &cobra.Command{
		Use:   "graph <agent>",
		Short: "Dump entities + relations (max 500)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodGet,
				withQuery(base+"/kg/graph", "user_id", user, "limit", positiveIntString(limit)), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	cmd.Flags().IntVar(&limit, "limit", 200, "max entities (server caps at 500)")
	return cmd
}

func kgTraverseCmd() *cobra.Command {
	var user string
	var depth int
	cmd := &cobra.Command{
		Use:   "traverse <agent> <entity-id>",
		Short: "Walk relations outward from an entity",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/kg/traverse",
				map[string]any{"entity_id": args[1], "user_id": user, "max_depth": depth}, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	cmd.Flags().IntVar(&depth, "depth", 2, "max traversal depth")
	return cmd
}

type kgUpsertOptions struct {
	ExternalID, Name, Type, Description, User, JSONFile string
	Props                                                []string
	Confidence                                           float64
}

func kgUpsertCmd() *cobra.Command {
	var o kgUpsertOptions
	cmd := &cobra.Command{
		Use:   "upsert <agent>",
		Short: "Create or update an entity (matched by --external-id)",
		Example: `  goclaw kg upsert gemie --external-id person:alice --name Alice --type person --prop role=CTO
  goclaw kg upsert gemie --json-file entity.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildKGEntityBody(o)
			if err != nil {
				return err
			}
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/kg/entities", body, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.ExternalID, "external-id", "", "stable external ID (upsert key)")
	f.StringVar(&o.Name, "name", "", "entity name")
	f.StringVar(&o.Type, "type", "", "entity type (person, project, ...)")
	f.StringVar(&o.Description, "description", "", "description")
	f.StringArrayVar(&o.Props, "prop", nil, "property key=value (repeatable)")
	f.Float64Var(&o.Confidence, "confidence", 0, "confidence 0..1 (server default 1.0)")
	f.StringVar(&o.User, "user", "", "user ID")
	f.StringVar(&o.JSONFile, "json-file", "", "entity JSON object; flags override its fields")
	return cmd
}

func buildKGEntityBody(o kgUpsertOptions) (map[string]any, error) {
	body := map[string]any{}
	if o.JSONFile != "" {
		raw, err := os.ReadFile(o.JSONFile)
		if err != nil {
			return nil, fmt.Errorf("read --json-file: %w", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("--json-file must contain a JSON object: %w", err)
		}
	}
	set := func(k, v string) {
		if v != "" {
			body[k] = v
		}
	}
	set("external_id", o.ExternalID)
	set("name", o.Name)
	set("entity_type", o.Type)
	set("description", o.Description)
	set("user_id", o.User)
	if o.Confidence > 0 {
		body["confidence"] = o.Confidence
	}
	if len(o.Props) > 0 {
		props := map[string]any{}
		if existing, ok := body["properties"].(map[string]any); ok {
			props = existing
		}
		for _, p := range o.Props {
			k, v, ok := strings.Cut(p, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return nil, fmt.Errorf("invalid --prop %q (want key=value)", p)
			}
			props[strings.TrimSpace(k)] = v
		}
		body["properties"] = props
	}
	for _, k := range []string{"external_id", "name", "entity_type"} {
		if s, _ := body[k].(string); s == "" {
			return nil, fmt.Errorf("%s is required (flag or --json-file)", k)
		}
	}
	return body, nil
}

func kgDeleteCmd() *cobra.Command {
	var user string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <agent> <entity-id>",
		Short: "Delete an entity and its relations (requires --force)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForce(force, "kg delete "+args[1]); err != nil {
				return err
			}
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodDelete,
				withQuery(base+"/kg/entities/"+url.PathEscape(args[1]), "user_id", user), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

func kgMergeCmd() *cobra.Command {
	var user, target, source string
	var force bool
	cmd := &cobra.Command{
		Use:   "merge <agent> --target <id> --source <id>",
		Short: "Merge source entity into target; source is removed (requires --force)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if target == "" || source == "" {
				return fmt.Errorf("--target and --source are required")
			}
			if target == source {
				return fmt.Errorf("--target and --source must differ")
			}
			if err := requireForce(force, "kg merge removes "+source); err != nil {
				return err
			}
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/kg/merge",
				map[string]any{"target_id": target, "source_id": source, "user_id": user}, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "entity ID to keep")
	cmd.Flags().StringVar(&source, "source", "", "entity ID to merge and remove")
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	cmd.Flags().BoolVar(&force, "force", false, "confirm merge")
	return cmd
}

func kgExtractCmd() *cobra.Command {
	var user, provider, model, text, fromFile string
	var minConf float64
	cmd := &cobra.Command{
		Use:   "extract <agent>",
		Short: "Extract entities/relations from text with an LLM and store them (slow, costs tokens)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "" || model == "" {
				return fmt.Errorf("--provider and --model are required")
			}
			if text == "" {
				content, err := readAgentFileContent(fromFile, os.Stdin, false)
				if err != nil {
					return err
				}
				text = content
			}
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/kg/extract", map[string]any{
				"text": text, "user_id": user, "provider": provider, "model": model, "min_confidence": minConf,
			}, slowDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "LLM provider name")
	cmd.Flags().StringVar(&model, "model", "", "model id")
	cmd.Flags().StringVar(&text, "text", "", "text to extract from (else --from-file or stdin)")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read text from file")
	cmd.Flags().Float64Var(&minConf, "min-confidence", 0, "drop results below this confidence")
	cmd.Flags().StringVar(&user, "user", "", "user ID")
	return cmd
}

func kgDedupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "dedup", Short: "Find and resolve duplicate entities"}

	var listUser string
	var listLimit int
	list := &cobra.Command{
		Use:   "list <agent>",
		Short: "List pending duplicate candidates",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodGet,
				withQuery(base+"/kg/dedup", "user_id", listUser, "limit", positiveIntString(listLimit)), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	list.Flags().StringVar(&listUser, "user", "", "user ID")
	list.Flags().IntVar(&listLimit, "limit", 0, "max candidates")

	var scanUser string
	var threshold float64
	var scanLimit int
	scan := &cobra.Command{
		Use:   "scan <agent>",
		Short: "Scan for duplicate candidates (similarity >= --threshold)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/kg/dedup/scan",
				map[string]any{"user_id": scanUser, "threshold": threshold, "limit": scanLimit}, slowDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	scan.Flags().StringVar(&scanUser, "user", "", "user ID")
	scan.Flags().Float64Var(&threshold, "threshold", 0.90, "similarity threshold")
	scan.Flags().IntVar(&scanLimit, "limit", 100, "max candidates to record")

	dismiss := &cobra.Command{
		Use:   "dismiss <agent> <candidate-id>",
		Short: "Mark a duplicate candidate as not-a-duplicate",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := agentPath(args[0])
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/kg/dedup/dismiss",
				map[string]any{"candidate_id": args[1]}, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}

	cmd.AddCommand(list, scan, dismiss)
	return cmd
}

// positiveIntString renders n for a query string, or "" (omitted) when n <= 0.
func positiveIntString(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

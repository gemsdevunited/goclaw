package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// goclaw vault — Knowledge Vault documents and wikilinks, via /v1/vault/* and
// /v1/agents/{id}/vault/*. Without --agent the tenant-wide routes are used.

const vaultDocTypesHelp = "context|memory|note|skill|episodic|media"
const vaultScopesHelp = "personal|team|shared"

func vaultCmd() *cobra.Command {
	return dataCmdGroup("vault", "Manage Knowledge Vault documents and links",
		vaultListCmd(), vaultGetCmd(), vaultTreeCmd(), vaultSearchCmd(), vaultLinksCmd(),
		vaultCreateCmd(), vaultUpdateCmd(), vaultDeleteCmd(), vaultLinkCmd(), vaultUnlinkCmd(),
		vaultRescanCmd(),
	)
}

// vaultBase returns the vault route prefix: agent-scoped when agentRef is set.
func vaultBase(agentRef string) (string, error) {
	if agentRef == "" {
		return "/v1/vault", nil
	}
	base, err := agentPath(agentRef)
	if err != nil {
		return "", err
	}
	return base + "/vault", nil
}

func addVaultAgentFlag(cmd *cobra.Command, dst *string) {
	cmd.Flags().StringVar(dst, "agent", "", "scope to this agent (key or UUID); default tenant-wide")
}

func vaultListCmd() *cobra.Command {
	var agent, scope, docType, team string
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List vault documents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := vaultBase(agent)
			if err != nil {
				return err
			}
			path := withQuery(base+"/documents", "scope", scope, "doc_type", docType, "team_id", team,
				"limit", positiveIntString(limit), "offset", positiveIntString(offset))
			raw, err := gatewayJSON(http.MethodGet, path, nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	cmd.Flags().StringVar(&scope, "scope", "", "filter: "+vaultScopesHelp)
	cmd.Flags().StringVar(&docType, "doc-type", "", "filter, comma-separated: "+vaultDocTypesHelp)
	cmd.Flags().StringVar(&team, "team", "", "filter by team ID")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size")
	cmd.Flags().IntVar(&offset, "offset", 0, "page offset")
	return cmd
}

func vaultGetCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "get <doc-id>",
		Short: "Show a vault document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := vaultBase(agent)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodGet, base+"/documents/"+url.PathEscape(args[0]), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	return cmd
}

func vaultTreeCmd() *cobra.Command {
	var agent, path, scope, docType string
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Browse the vault as a folder tree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			agentID := ""
			if agent != "" {
				id, err := resolveAgentID(agent)
				if err != nil {
					return err
				}
				agentID = id
			}
			raw, err := gatewayJSON(http.MethodGet, withQuery("/v1/vault/tree",
				"path", path, "agent_id", agentID, "scope", scope, "doc_type", docType), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	cmd.Flags().StringVar(&path, "path", "", "folder to list")
	cmd.Flags().StringVar(&scope, "scope", "", "filter: "+vaultScopesHelp)
	cmd.Flags().StringVar(&docType, "doc-type", "", "filter, comma-separated: "+vaultDocTypesHelp)
	return cmd
}

func vaultSearchCmd() *cobra.Command {
	var agent, scope, docType string
	var maxResults int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Hybrid (keyword + semantic) search over the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := vaultBase(agent)
			if err != nil {
				return err
			}
			body := map[string]any{"query": args[0], "scope": scope, "max_results": maxResults}
			if docType != "" {
				body["doc_types"] = strings.Split(docType, ",")
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/search", body, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	cmd.Flags().StringVar(&scope, "scope", "", "filter: "+vaultScopesHelp)
	cmd.Flags().StringVar(&docType, "doc-type", "", "filter, comma-separated: "+vaultDocTypesHelp)
	cmd.Flags().IntVar(&maxResults, "max-results", 10, "maximum results")
	return cmd
}

func vaultLinksCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "links <doc-id>",
		Short: "Show outgoing and incoming links of a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := vaultBase(agent)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodGet, base+"/documents/"+url.PathEscape(args[0])+"/links", nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	return cmd
}

func vaultCreateCmd() *cobra.Command {
	var agent, path, title, docType, scope, team, fromFile string
	cmd := &cobra.Command{
		Use:   "create --path <rel/path.md> --title <title>",
		Short: "Register a vault document; --from-file also writes its content",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if path == "" || title == "" {
				return fmt.Errorf("--path and --title are required")
			}
			body := map[string]any{"path": path, "title": title, "doc_type": docType, "scope": scope, "team_id": team}
			if fromFile != "" {
				raw, err := os.ReadFile(fromFile)
				if err != nil {
					return err
				}
				body["content"] = string(raw)
			}
			base, err := vaultBase(agent)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/documents", body, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	cmd.Flags().StringVar(&path, "path", "", "relative path inside the workspace")
	cmd.Flags().StringVar(&title, "title", "", "document title")
	cmd.Flags().StringVar(&docType, "doc-type", "", "type (default note): "+vaultDocTypesHelp)
	cmd.Flags().StringVar(&scope, "scope", "", "scope (default personal): "+vaultScopesHelp)
	cmd.Flags().StringVar(&team, "team", "", "team ID for team scope")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "write this file's content to the document")
	return cmd
}

func vaultUpdateCmd() *cobra.Command {
	var agent, title, docType, scope, team, fromFile string
	cmd := &cobra.Command{
		Use:   "update <doc-id>",
		Short: "Update document metadata and/or content (only passed flags change)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			fl := cmd.Flags()
			for flag, key := range map[string]string{"title": "title", "doc-type": "doc_type", "scope": "scope", "team": "team_id"} {
				if fl.Changed(flag) {
					v, _ := fl.GetString(flag)
					body[key] = v
				}
			}
			if fromFile != "" {
				raw, err := os.ReadFile(fromFile)
				if err != nil {
					return err
				}
				body["content"] = string(raw)
			}
			if len(body) == 0 {
				return fmt.Errorf("nothing to update (pass --title, --doc-type, --scope, --team or --from-file)")
			}
			base, err := vaultBase(agent)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPut, base+"/documents/"+url.PathEscape(args[0]), body, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&docType, "doc-type", "", "new type: "+vaultDocTypesHelp)
	cmd.Flags().StringVar(&scope, "scope", "", "new scope: "+vaultScopesHelp)
	cmd.Flags().StringVar(&team, "team", "", "team ID (\"\" clears)")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "replace content with this file")
	return cmd
}

func vaultDeleteCmd() *cobra.Command {
	var agent string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <doc-id>",
		Short: "Delete a vault document (requires --force)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForce(force, "vault delete "+args[0]); err != nil {
				return err
			}
			base, err := vaultBase(agent)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodDelete, base+"/documents/"+url.PathEscape(args[0]), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

func vaultLinkCmd() *cobra.Command {
	var agent, from, to, linkType, linkContext string
	cmd := &cobra.Command{
		Use:   "link --from <doc-id> --to <doc-id>",
		Short: "Create a link between two documents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" || to == "" {
				return fmt.Errorf("--from and --to are required")
			}
			base, err := vaultBase(agent)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodPost, base+"/links", map[string]any{
				"from_doc_id": from, "to_doc_id": to, "link_type": linkType, "context": linkContext,
			}, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	cmd.Flags().StringVar(&from, "from", "", "source document ID")
	cmd.Flags().StringVar(&to, "to", "", "target document ID")
	cmd.Flags().StringVar(&linkType, "type", "", "link type (default reference)")
	cmd.Flags().StringVar(&linkContext, "context", "", "surrounding text / note")
	return cmd
}

func vaultUnlinkCmd() *cobra.Command {
	var agent string
	var force bool
	cmd := &cobra.Command{
		Use:   "unlink <link-id>",
		Short: "Delete a link (requires --force)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForce(force, "vault unlink "+args[0]); err != nil {
				return err
			}
			base, err := vaultBase(agent)
			if err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodDelete, base+"/links/"+url.PathEscape(args[0]), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	addVaultAgentFlag(cmd, &agent)
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

func vaultRescanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rescan",
		Short: "Re-sync the vault registry with workspace files (slow)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := gatewayJSON(http.MethodPost, "/v1/vault/rescan", map[string]any{}, slowDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
}

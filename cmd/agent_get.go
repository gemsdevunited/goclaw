package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// agentConfigSections are the JSONB config blocks of an agent. Empty/absent
// means "not overridden per agent" — the runtime then falls back to the global
// config.json value (see internal/agent/resolver.go), NOT necessarily to the
// hardcoded source defaults.
var agentConfigSections = []string{
	"compaction_config",
	"context_pruning",
	"memory_config",
	"tools_config",
	"subagents_config",
	"sandbox_config",
	"reasoning_config",
	"model_fallback",
	"shell_deny_groups",
	"workspace_sharing",
	"chatgpt_oauth_routing",
	"kg_dedup_config",
	"other_config",
}

func agentGetCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "get <agent-key|agent-id>",
		Short: "Show an agent's full config (requires running gateway)",
		Long: `Show the stored config of one agent via GET /v1/agents/{id|key}.

Prints core fields plus every per-agent config block (compaction_config,
context_pruning, memory_config, tools_config, ...). A block shown as
"(unset)" is not overridden for this agent: the gateway uses the global
config.json value, falling back to built-in defaults only when that is unset
too. Use --json for the raw API response.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			requireRunningGatewayHTTP()
			resp, err := gatewayHTTPGet("/v1/agents/" + url.PathEscape(args[0]))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if jsonOutput {
				data, _ := json.MarshalIndent(resp, "", "  ")
				fmt.Println(string(data))
				return
			}
			printAgentDetail(os.Stdout, resp)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output raw JSON")
	return cmd
}

// printAgentDetail renders the agent response: scalar fields as a table,
// config blocks as indented JSON.
func printAgentDetail(out io.Writer, a map[string]any) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, k := range []string{
		"id", "agent_key", "display_name", "agent_type", "status", "is_default",
		"provider", "model", "context_window", "max_tool_iterations", "max_tokens",
		"thinking_level", "budget_monthly_cents", "self_evolve", "skill_evolve",
		"workspace", "restrict_to_workspace", "tenant_id", "owner_id",
	} {
		fmt.Fprintf(w, "%s:\t%s\n", k, scalarString(a[k]))
	}
	w.Flush()

	for _, k := range agentConfigSections {
		v, ok := a[k]
		if !ok || v == nil || isEmptyJSON(v) {
			fmt.Fprintf(out, "\n%s: (unset — uses global config.json / built-in default)\n", k)
			continue
		}
		data, _ := json.MarshalIndent(v, "  ", "  ")
		fmt.Fprintf(out, "\n%s:\n  %s\n", k, data)
	}
}

func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		if x == "" {
			return "-"
		}
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	default:
		return fmt.Sprint(x)
	}
}

func isEmptyJSON(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	case string:
		s := strings.TrimSpace(x)
		return s == "" || s == "null" || s == "{}"
	}
	return false
}

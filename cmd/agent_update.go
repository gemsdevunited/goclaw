package cmd

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// agentUpdateOptions holds the flags of `goclaw agent update`.
// Pointer/changed-tracking is done via cobra's Flags().Changed so that an
// explicit zero value (e.g. --max-tokens 0) is still sent.
type agentUpdateOptions struct {
	DisplayName string
	Provider    string
	Model       string
	Status      string
	Description string
	Emoji       string
	Thinking    string
	ContextWin  int
	MaxIter     int
	MaxTokens   int
	BudgetCents int
	SelfEvolve  bool
	SetPairs    []string // key=<json value>
	JSONFile    string   // file with a JSON object merged into the body
}

func agentUpdateCmd() *cobra.Command {
	var o agentUpdateOptions
	cmd := &cobra.Command{
		Use:   "update <agent-key|agent-id>",
		Short: "Update an agent's config (requires running gateway, admin role)",
		Long: `Update fields of an existing agent via PUT /v1/agents/{id}.

Only the flags you pass are sent. Use --set key=<json> for any other field
allowed by the server (tools_config, other_config, subagents_config,
compaction_config, memory_config, ... — values are parsed as JSON), or
--json-file to merge a whole JSON object. Values that are not valid JSON are
sent as plain strings; quote string values that look like JSON
(e.g. --set 'model="123"').

Examples:
  goclaw agent update gemie --model MiniMax-M3 --provider minimax-native
  goclaw agent update gemie --set 'tools_config={"deny":["exec"]}'
  goclaw agent update gemie --json-file patch.json`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			requireRunningGatewayHTTP()
			body, err := buildAgentUpdateBody(cmd, o)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if len(body) == 0 {
				fmt.Fprintln(os.Stderr, "Error: nothing to update (pass at least one flag, --set or --json-file)")
				os.Exit(1)
			}
			id, err := resolveAgentID(args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if _, err := gatewayHTTPPut("/v1/agents/"+url.PathEscape(id), body); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating agent: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Agent %q updated (%s).\n", args[0], strings.Join(sortedKeys(body), ", "))
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.DisplayName, "display-name", "", "display name")
	f.StringVar(&o.Provider, "provider", "", "LLM provider name")
	f.StringVar(&o.Model, "model", "", "model id")
	f.StringVar(&o.Status, "status", "", "status (active|inactive)")
	f.StringVar(&o.Description, "description", "", "agent description")
	f.StringVar(&o.Emoji, "emoji", "", "emoji")
	f.StringVar(&o.Thinking, "thinking-level", "", "thinking level")
	f.IntVar(&o.ContextWin, "context-window", 0, "context window tokens")
	f.IntVar(&o.MaxIter, "max-tool-iterations", 0, "max tool iterations per run")
	f.IntVar(&o.MaxTokens, "max-tokens", 0, "max output tokens")
	f.IntVar(&o.BudgetCents, "budget-monthly-cents", 0, "monthly budget in cents")
	f.BoolVar(&o.SelfEvolve, "self-evolve", false, "enable self-evolution")
	f.StringArrayVar(&o.SetPairs, "set", nil, "set arbitrary field: key=<json value> (repeatable)")
	f.StringVar(&o.JSONFile, "json-file", "", "merge a JSON object from file into the update body")
	return cmd
}

// buildAgentUpdateBody assembles the PUT body from changed flags, --json-file
// and --set pairs (applied last, so they win).
func buildAgentUpdateBody(cmd *cobra.Command, o agentUpdateOptions) (map[string]any, error) {
	body := map[string]any{}
	fl := cmd.Flags()
	str := func(flag, key, val string) {
		if fl.Changed(flag) {
			body[key] = val
		}
	}
	num := func(flag, key string, val int) {
		if fl.Changed(flag) {
			body[key] = val
		}
	}
	str("display-name", "display_name", o.DisplayName)
	str("provider", "provider", o.Provider)
	str("model", "model", o.Model)
	str("status", "status", o.Status)
	str("description", "agent_description", o.Description)
	str("emoji", "emoji", o.Emoji)
	str("thinking-level", "thinking_level", o.Thinking)
	num("context-window", "context_window", o.ContextWin)
	num("max-tool-iterations", "max_tool_iterations", o.MaxIter)
	num("max-tokens", "max_tokens", o.MaxTokens)
	num("budget-monthly-cents", "budget_monthly_cents", o.BudgetCents)
	if fl.Changed("self-evolve") {
		body["self_evolve"] = o.SelfEvolve
	}

	if o.JSONFile != "" {
		raw, err := os.ReadFile(o.JSONFile)
		if err != nil {
			return nil, fmt.Errorf("read --json-file: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("--json-file must contain a JSON object: %w", err)
		}
		maps.Copy(body, m)
	}
	for _, p := range o.SetPairs {
		k, v, ok := strings.Cut(p, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --set %q (want key=<json value>)", p)
		}
		var val any
		if err := json.Unmarshal([]byte(v), &val); err != nil {
			// Bare word: treat as plain string for convenience (--set model=gpt-5).
			val = v
		}
		body[k] = val
	}
	return body, nil
}

// resolveAgentID accepts an agent UUID or an agent_key and returns the UUID.
// The HTTP update endpoint only accepts UUIDs.
func resolveAgentID(ref string) (string, error) {
	if _, err := uuid.Parse(ref); err == nil {
		return ref, nil
	}
	resp, err := gatewayHTTPGet("/v1/agents")
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(resp["agents"])
	var agents []httpAgent
	if err := json.Unmarshal(raw, &agents); err != nil {
		return "", fmt.Errorf("parse agent list: %w", err)
	}
	for _, a := range agents {
		if a.AgentKey == ref {
			return a.ID, nil
		}
	}
	return "", fmt.Errorf("agent %q not found", ref)
}

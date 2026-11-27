package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func toolsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Manage built-in tool settings — list, get, set",
	}
	cmd.AddCommand(toolsListCmd())
	cmd.AddCommand(toolsGetCmd())
	cmd.AddCommand(toolsSetCmd())
	return cmd
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func toolsListCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List built-in tools with enabled state (requires running gateway)",
		Run: func(cmd *cobra.Command, args []string) {
			requireRunningGatewayHTTP()
			resp, err := gatewayHTTPGet("/v1/tools/builtin")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			list, _ := resp["tools"].([]any)
			if jsonOutput {
				data, _ := json.MarshalIndent(list, "", "  ")
				fmt.Println(string(data))
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tCATEGORY\tENABLED\tTENANT")
			for _, it := range list {
				m, _ := it.(map[string]any)
				tenant := "-"
				if v, ok := m["tenant_enabled"].(bool); ok {
					tenant = fmt.Sprint(v)
				}
				fmt.Fprintf(w, "%v\t%v\t%v\t%s\n", m["name"], m["category"], m["enabled"], tenant)
			}
			w.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func toolsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Show one built-in tool (settings, enabled)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			requireRunningGatewayHTTP()
			resp, err := gatewayHTTPGet("/v1/tools/builtin/" + url.PathEscape(args[0]))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			data, _ := json.MarshalIndent(resp, "", "  ")
			fmt.Println(string(data))
		},
	}
}

func toolsSetCmd() *cobra.Command {
	var enabled bool
	var settings string
	var global bool
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Enable/disable a built-in tool or change its settings",
		Long: `Change a built-in tool. By default writes the per-tenant override
(PUT /v1/tools/builtin/{name}/tenant-config, tenant admin). With --global it
writes the shared definition (PUT /v1/tools/builtin/{name}, master scope only).

Examples:
  goclaw tools set web_search --enabled=false
  goclaw tools set web_search --settings '{"provider_order":["brave"]}'`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			requireRunningGatewayHTTP()
			body, err := buildToolsSetBody(cmd.Flags().Changed("enabled"), enabled, settings)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			path := "/v1/tools/builtin/" + url.PathEscape(args[0])
			if !global {
				path += "/tenant-config"
			}
			if _, err := gatewayHTTPPut(path, body); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating tool: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Tool %q updated (%v).\n", args[0], sortedKeys(body))
		},
	}
	cmd.Flags().BoolVar(&enabled, "enabled", true, "enable (true) or disable (false) the tool")
	cmd.Flags().StringVar(&settings, "settings", "", "tool settings as a JSON object")
	cmd.Flags().BoolVar(&global, "global", false, "write the global definition instead of the tenant override (master scope)")
	return cmd
}

func buildToolsSetBody(enabledChanged, enabled bool, settings string) (map[string]any, error) {
	body := map[string]any{}
	if enabledChanged {
		body["enabled"] = enabled
	}
	if settings != "" {
		var v map[string]any
		if err := json.Unmarshal([]byte(settings), &v); err != nil {
			return nil, fmt.Errorf("--settings must be a JSON object: %w", err)
		}
		body["settings"] = v
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("nothing to update (pass --enabled and/or --settings)")
	}
	return body, nil
}

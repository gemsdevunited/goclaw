package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func agentFilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "View/edit agent-level context files (SOUL.md, IDENTITY.md, ...)",
	}
	cmd.AddCommand(agentFilesListCmd(), agentFilesGetCmd(), agentFilesSetCmd())
	return cmd
}

func agentFilesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <agent-key|agent-id>",
		Short: "List context files of an agent and their sizes",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			requireRunningGatewayHTTP()
			id := mustResolveAgentID(args[0])
			resp, err := gatewayHTTPGet("/v1/agents/" + url.PathEscape(id) + "/files")
			if err != nil {
				fatalf("Error: %v", err)
			}
			list, _ := resp["files"].([]any)
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "FILE\tSTATUS\tSIZE")
			for _, it := range list {
				m, _ := it.(map[string]any)
				status, size := "set", fmt.Sprint(m["size"])
				if missing, _ := m["missing"].(bool); missing {
					status, size = "missing", "-"
				}
				fmt.Fprintf(w, "%v\t%s\t%s\n", m["name"], status, size)
			}
			w.Flush()
		},
	}
}

func agentFilesGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <agent-key|agent-id> <file>",
		Short: "Print the content of a context file (e.g. SOUL.md)",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			requireRunningGatewayHTTP()
			id := mustResolveAgentID(args[0])
			resp, err := gatewayHTTPGet("/v1/agents/" + url.PathEscape(id) + "/files/" + url.PathEscape(args[1]))
			if err != nil {
				fatalf("Error: %v", err)
			}
			if missing, _ := resp["missing"].(bool); missing {
				fatalf("File %s is not set for this agent.", args[1])
			}
			fmt.Print(resp["content"])
		},
	}
}

func agentFilesSetCmd() *cobra.Command {
	var fromFile string
	var propagate, allowEmpty bool
	cmd := &cobra.Command{
		Use:   "set <agent-key|agent-id> <file>",
		Short: "Replace a context file; content from --from-file or stdin",
		Long: `Replace an agent-level context file. Content is read from --from-file,
or from piped stdin when omitted. Empty content is refused unless --allow-empty
is passed, so a missing input never wipes SOUL.md by accident.
Use --propagate to push the change to existing per-user copies.

Example:
  goclaw agent files get gemie SOUL.md > soul.md   # edit, then:
  goclaw agent files set gemie SOUL.md --from-file soul.md`,
		Args: cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			requireRunningGatewayHTTP()
			content, err := readAgentFileContent(fromFile, os.Stdin, allowEmpty)
			if err != nil {
				fatalf("Error: %v", err)
			}
			id := mustResolveAgentID(args[0])
			resp, err := gatewayHTTPPut("/v1/agents/"+url.PathEscape(id)+"/files/"+url.PathEscape(args[1]),
				map[string]any{"content": content, "propagate": propagate})
			if err != nil {
				fatalf("Error: %v", err)
			}
			out, _ := json.Marshal(resp)
			fmt.Printf("Updated %s for %s: %s\n", args[1], args[0], out)
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read content from file (default: stdin)")
	cmd.Flags().BoolVar(&propagate, "propagate", false, "push the change to existing per-user copies")
	cmd.Flags().BoolVar(&allowEmpty, "allow-empty", false, "allow writing an empty file")
	return cmd
}

// readAgentFileContent returns the new file content from fromFile, or from stdin
// when fromFile is empty. Stdin attached to a terminal is rejected instead of
// blocking forever; empty content is rejected unless allowEmpty is set.
func readAgentFileContent(fromFile string, stdin *os.File, allowEmpty bool) (string, error) {
	var raw []byte
	var err error
	if fromFile != "" {
		raw, err = os.ReadFile(fromFile)
	} else {
		if fi, statErr := stdin.Stat(); statErr == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return "", fmt.Errorf("no content: pass --from-file or pipe content via stdin")
		}
		raw, err = io.ReadAll(stdin)
	}
	if err != nil {
		return "", fmt.Errorf("read content: %w", err)
	}
	if len(raw) == 0 && !allowEmpty {
		return "", fmt.Errorf("content is empty; pass --allow-empty to clear the file")
	}
	return string(raw), nil
}

func mustResolveAgentID(ref string) string {
	id, err := resolveAgentID(ref)
	if err != nil {
		fatalf("Error: %v", err)
	}
	return id
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

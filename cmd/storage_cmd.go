package cmd

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"
)

// goclaw storage — tenant workspace files, via /v1/storage/*.
// Writes (upload, move, delete) require a tenant admin on the server side.

func storageCmd() *cobra.Command {
	return dataCmdGroup("storage", "Manage workspace files — list, get, size, upload, move, delete",
		storageListCmd(), storageGetCmd(), storageSizeCmd(),
		storageUploadCmd(), storageMoveCmd(), storageDeleteCmd(),
	)
}

func storageListCmd() *cobra.Command {
	var path string
	var depth int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List files and folders",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := ""
			if cmd.Flags().Changed("depth") {
				d = strconv.Itoa(depth)
			}
			raw, err := gatewayJSON(http.MethodGet, withQuery("/v1/storage/files", "path", path, "depth", d), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "folder to list (default root)")
	cmd.Flags().IntVar(&depth, "depth", 1, "folder depth")
	return cmd
}

func storageGetCmd() *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "get <path>",
		Short: "Read a file (JSON with content, or --raw bytes to stdout)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/storage/files/" + escapePathSegments(args[0])
			if raw {
				body, err := gatewayJSON(http.MethodGet, withQuery(path, "raw", "true"), nil, defaultDataTimeout)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(body)
				return err
			}
			body, err := gatewayJSON(http.MethodGet, path, nil, defaultDataTimeout)
			return emitJSON(cmd, body, err)
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "write the raw file bytes to stdout")
	return cmd
}

func storageSizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "size",
		Short: "Total size and file count of the workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := gatewayJSON(http.MethodGet, "/v1/storage/size", nil, slowDataTimeout)
			if err != nil {
				return err
			}
			last, err := lastSSEData(body)
			return emitJSON(cmd, last, err)
		},
	}
}

func storageUploadCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "upload <local-file>",
		Short: "Upload a local file into a workspace folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := gatewayUpload(withQuery("/v1/storage/files", "path", dir), args[0], slowDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().StringVar(&dir, "path", "", "destination folder (default root)")
	return cmd
}

func storageMoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "move <from> <to>",
		Short: "Move/rename a file or folder (never overwrites)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := gatewayJSON(http.MethodPut, withQuery("/v1/storage/move", "from", args[0], "to", args[1]), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
}

func storageDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <path>",
		Short: "Delete a file or folder recursively (requires --force)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireForce(force, fmt.Sprintf("storage delete %s", args[0])); err != nil {
				return err
			}
			raw, err := gatewayJSON(http.MethodDelete, "/v1/storage/files/"+escapePathSegments(args[0]), nil, defaultDataTimeout)
			return emitJSON(cmd, raw, err)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

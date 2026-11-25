package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Helpers for the data-management CLI groups (memory, kg, vault, storage).
// Unlike gatewayHTTPDo they keep the raw JSON (many endpoints return arrays),
// support per-command timeouts, and return errors so commands can use RunE.

const (
	defaultDataTimeout = 30 * time.Second
	slowDataTimeout    = 10 * time.Minute // indexing, extraction, rescans
	dataResponseLimit  = 32 << 20
)

// gatewayJSON sends a JSON request and returns the raw response body.
// HTTP status >= 400 is turned into a *gatewayHTTPError.
func gatewayJSON(method, path string, body any, timeout time.Duration) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := newGatewayRequest(method, path, reader, "application/json")
	if err != nil {
		return nil, err
	}
	return sendGatewayData(req, timeout)
}

// gatewayUpload posts localPath as multipart field "file".
func gatewayUpload(path, localPath string, timeout time.Duration) (json.RawMessage, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(localPath))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := newGatewayRequest(http.MethodPost, path, &buf, mw.FormDataContentType())
	if err != nil {
		return nil, err
	}
	return sendGatewayData(req, timeout)
}

func sendGatewayData(req *http.Request, timeout time.Duration) (json.RawMessage, error) {
	raw, status, err := doGatewayRequest(&http.Client{Timeout: timeout}, req, dataResponseLimit)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseHTTPError(raw, status)
	}
	return raw, nil
}

// printRawJSON writes raw JSON indented to w; non-JSON bodies are written as-is.
func printRawJSON(w io.Writer, raw []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		_, werr := w.Write(raw)
		return werr
	}
	buf.WriteByte('\n')
	_, err := buf.WriteTo(w)
	return err
}

// emitJSON is the common tail of a data command: print the response or return the error.
func emitJSON(cmd *cobra.Command, raw json.RawMessage, err error) error {
	if err != nil {
		return err
	}
	return printRawJSON(cmd.OutOrStdout(), raw)
}

// withQuery appends key/value pairs to path, skipping empty values.
func withQuery(path string, kv ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			q.Set(kv[i], kv[i+1])
		}
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// escapePathSegments escapes each segment of a slash-separated relative path
// for use in a `{path...}` route while keeping the separators.
func escapePathSegments(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

var errForceRequired = errors.New("refusing destructive operation without --force")

// requireForce guards destructive commands; agents and scripts must opt in explicitly.
func requireForce(force bool, what string) error {
	if !force {
		return fmt.Errorf("%w: %s", errForceRequired, what)
	}
	return nil
}

// agentPath resolves an agent key/UUID and returns "/v1/agents/{uuid}".
func agentPath(ref string) (string, error) {
	id, err := resolveAgentID(ref)
	if err != nil {
		return "", err
	}
	return "/v1/agents/" + url.PathEscape(id), nil
}

// lastSSEData returns the payload of the last `data:` line of an SSE body.
func lastSSEData(raw []byte) (json.RawMessage, error) {
	var last []byte
	for line := range bytes.SplitSeq(raw, []byte("\n")) {
		if d, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
			last = bytes.TrimSpace(d)
		}
	}
	if last == nil {
		return nil, errors.New("no data in event stream")
	}
	return last, nil
}

// dataCmdGroup builds a parent command that requires a running gateway for all
// children and does not dump usage when a request fails.
func dataCmdGroup(use, short string, children ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			requireRunningGatewayHTTP()
		},
	}
	cmd.AddCommand(children...)
	silenceUsage(cmd)
	return cmd
}

func silenceUsage(cmd *cobra.Command) {
	cmd.SilenceUsage = true
	for _, c := range cmd.Commands() {
		silenceUsage(c)
	}
}

package mcp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
)

func TestIsTransportErr(t *testing.T) {
	activeCtx := context.Background()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel parent context immediately

	cases := []struct {
		name      string
		err       error
		parentCtx context.Context
		want      bool
	}{
		{"nil error", nil, activeCtx, false},
		{"parent context canceled (user stop)", context.Canceled, canceledCtx, false},
		{"io.EOF", io.EOF, activeCtx, true},
		{"net error", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, activeCtx, true},
		{"url error", &url.Error{Op: "Get", URL: "http://localhost:8080", Err: io.EOF}, activeCtx, true},
		{"string connection refused", errors.New("connection refused"), activeCtx, true},
		{"string connection reset", errors.New("connection reset by peer"), activeCtx, true},
		{"string transport error", errors.New("transport: stream closed"), activeCtx, true},
		{"string broken pipe", errors.New("write: broken pipe"), activeCtx, true},
		{"unrelated error", errors.New("invalid parameter 'age'"), activeCtx, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransportErr(tc.err, tc.parentCtx)
			if got != tc.want {
				t.Errorf("isTransportErr(%v, ctx) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestBridgeTool_TransportError_CircuitBreaker(t *testing.T) {
	clientPtr := &atomic.Pointer[mcpclient.Client]{}
	clientPtr.Store(&mcpclient.Client{})

	connected := &atomic.Bool{}
	connected.Store(true)

	var forceReconnectCalled atomic.Bool
	var forceReconnectReason string

	bt := &BridgeTool{
		serverName:     "test-server",
		toolName:       "get_data",
		registeredName: "mcp_test-server_get_data",
		timeoutSec:     5,
		clientPtr:      clientPtr,
		connected:      connected,
		forceReconnect: func(reason string) {
			forceReconnectCalled.Store(true)
			forceReconnectReason = reason
		},
	}

	// Simulate transport error detection logic
	ctx := context.Background()
	transportErr := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}

	if isTransportErr(transportErr, ctx) {
		bt.connected.Store(false)
		if bt.forceReconnect != nil {
			bt.forceReconnect("bridge_tool transport: " + bt.registeredName)
		}
	}

	// Assertions
	if bt.IsConnected() {
		t.Errorf("expected BridgeTool.IsConnected() to be false after transport error, got true")
	}

	if !forceReconnectCalled.Load() {
		t.Errorf("expected forceReconnect callback to be called")
	}

	if !strings.Contains(forceReconnectReason, "bridge_tool transport") {
		t.Errorf("expected forceReconnect reason to contain 'bridge_tool transport', got %q", forceReconnectReason)
	}

	// Execute next tool call — should short-circuit at line 204 (connected == false)
	res := bt.Execute(ctx, map[string]any{})
	if res == nil || !res.IsError {
		t.Fatalf("expected error result on disconnected tool call, got %v", res)
	}

	if !strings.Contains(res.ForLLM, "disconnected") {
		t.Errorf("expected short-circuit message to contain 'disconnected', got %q", res.ForLLM)
	}
}

func TestBridgeTool_UserCanceled_NoReconnect(t *testing.T) {
	clientPtr := &atomic.Pointer[mcpclient.Client]{}
	clientPtr.Store(&mcpclient.Client{})

	connected := &atomic.Bool{}
	connected.Store(true)

	var forceReconnectCalled atomic.Bool

	bt := &BridgeTool{
		serverName:     "test-server",
		toolName:       "get_data",
		registeredName: "mcp_test-server_get_data",
		timeoutSec:     5,
		clientPtr:      clientPtr,
		connected:      connected,
		forceReconnect: func(reason string) {
			forceReconnectCalled.Store(true)
		},
	}

	// Canceled parent context (User clicked stop)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := context.Canceled

	if isTransportErr(err, canceledCtx) {
		bt.connected.Store(false)
		if bt.forceReconnect != nil {
			bt.forceReconnect("bridge_tool transport: " + bt.registeredName)
		}
	}

	// Assertions: Connected remains true and forceReconnect is NOT triggered
	if !bt.IsConnected() {
		t.Errorf("expected BridgeTool to remain connected when user cancels turn, got disconnected")
	}

	if forceReconnectCalled.Load() {
		t.Errorf("expected forceReconnect NOT to be called when user cancels turn")
	}
}

func TestWaitForReconnect(t *testing.T) {
	connected := &atomic.Bool{}
	connected.Store(false)

	// Simulate async reconnection completing after 50ms
	go func() {
		<-time.After(50 * time.Millisecond)
		connected.Store(true)
	}()

	ctx := context.Background()
	got := waitForReconnect(ctx, connected, 300*time.Millisecond)
	if !got {
		t.Errorf("expected waitForReconnect to return true when connected flips to true within timeout")
	}

	// Fast fail test when timeout exceeded
	connected.Store(false)
	gotTimeout := waitForReconnect(ctx, connected, 50*time.Millisecond)
	if gotTimeout {
		t.Errorf("expected waitForReconnect to return false when connection remains down")
	}
}


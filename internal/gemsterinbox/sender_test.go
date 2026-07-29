package gemsterinbox

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/outbounddelivery"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestClientSendsSignedCronDelivery(t *testing.T) {
	client, err := NewClient("https://gemster.example/", "key-1", "secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://gemster.example"+deliveryPath {
			t.Fatalf("URL = %s", req.URL)
		}
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Fatalf("read body: %v", readErr)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload["deliveryId"] != "delivery-1" || payload["recipientEmail"] != "user@example.com" {
			t.Fatalf("payload = %#v", payload)
		}

		timestamp, _ := strconv.ParseInt(req.Header.Get(headerTimestamp), 10, 64)
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write([]byte("v1.key-1." + strconv.FormatInt(timestamp, 10) + "."))
		_, _ = mac.Write(body)
		wantSignature := "v1=" + hex.EncodeToString(mac.Sum(nil))
		if req.Header.Get(headerSignature) != wantSignature {
			t.Fatalf("signature = %q, want %q", req.Header.Get(headerSignature), wantSignature)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Body:       io.NopCloser(strings.NewReader(`{"status":"created"}`)),
			Header:     make(http.Header),
		}, nil
	})

	err = client.Send(context.Background(), outbounddelivery.Delivery{
		ID:         "delivery-1",
		TenantID:   uuid.New(),
		Recipient:  "user@example.com",
		SourceKind: outbounddelivery.SourceCron,
		SourceID:   "execution-1",
		Title:      "Report",
		Body:       "Report body",
		CreatedAt:  time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestClientRejectsNonSuccessStatus(t *testing.T) {
	client, err := NewClient("https://gemster.example", "key-1", "secret", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("unavailable")),
			Header:     make(http.Header),
		}, nil
	})

	err = client.Send(context.Background(), outbounddelivery.Delivery{
		ID:         "delivery-1",
		TenantID:   uuid.New(),
		Recipient:  "user@example.com",
		SourceKind: outbounddelivery.SourceCron,
		SourceID:   "execution-1",
		Title:      "Report",
		Body:       "Report body",
		CreatedAt:  time.Now(),
	})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Send error = %v, want status error", err)
	}
}

func TestNewFromEnv(t *testing.T) {
	t.Setenv("GEMSTER_INBOX_DELIVERY_ENABLED", "true")
	t.Setenv("GEMSTER_INBOX_DELIVERY_ENDPOINT", "https://gemster.example")
	t.Setenv("GEMSTER_INBOX_DELIVERY_HMAC_KEY_ID", "key-1")
	t.Setenv("GEMSTER_INBOX_HMAC_KEYS_JSON", `{"key-1":"secret"}`)

	sender, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	client, ok := sender.(*Client)
	if !ok || client.keyID != "key-1" || client.secret != "secret" {
		t.Fatalf("sender = %#v", sender)
	}
}

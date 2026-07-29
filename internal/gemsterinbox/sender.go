package gemsterinbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/outbounddelivery"
)

const (
	Destination = "gemster_inbox"

	headerKeyID     = "X-GoClaw-Key-Id"
	headerTimestamp = "X-GoClaw-Timestamp"
	headerSignature = "X-GoClaw-Signature"

	deliveryPath = "/internal/goclaw/gemster-inbox/deliveries"
)

// Client is the Gemster Inbox delivery adapter.
type Client struct {
	endpoint   string
	keyID      string
	secret     string
	httpClient *http.Client
}

var _ outbounddelivery.Sender = (*Client)(nil)

func NewClient(endpoint, keyID, secret string, timeout time.Duration) (*Client, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	keyID = strings.TrimSpace(keyID)
	if endpoint == "" || keyID == "" || secret == "" {
		return nil, errors.New("gemster inbox: endpoint, key ID, and secret are required")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{
		endpoint: endpoint,
		keyID:    keyID,
		secret:   secret,
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// NewFromEnv returns nil when Gemster Inbox delivery is disabled.
func NewFromEnv() (outbounddelivery.Sender, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("GEMSTER_INBOX_DELIVERY_ENABLED")), "false") {
		return nil, nil
	}
	keyID := strings.TrimSpace(os.Getenv("GEMSTER_INBOX_DELIVERY_HMAC_KEY_ID"))
	secret, err := secretFromEnv(&keyID)
	if err != nil || secret == "" {
		return nil, nil
	}
	timeout := 15 * time.Second
	if raw := strings.TrimSpace(os.Getenv("GEMSTER_INBOX_DELIVERY_TIMEOUT")); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("gemster inbox: invalid timeout: %w", parseErr)
		}
		timeout = parsed
	}
	return NewClient(
		os.Getenv("GEMSTER_INBOX_DELIVERY_ENDPOINT"),
		keyID,
		secret,
		timeout,
	)
}

func secretFromEnv(keyID *string) (string, error) {
	if simpleSecret := strings.TrimSpace(os.Getenv("GOCLAW_HMAC_SECRET")); simpleSecret != "" {
		if *keyID == "" {
			*keyID = "default"
		}
		return simpleSecret, nil
	}
	if simpleSecret := strings.TrimSpace(os.Getenv("GEMSTER_INBOX_HMAC_SECRET")); simpleSecret != "" {
		if *keyID == "" {
			*keyID = "default"
		}
		return simpleSecret, nil
	}
	if simpleSecret := strings.TrimSpace(os.Getenv("GEMSTER_INBOX_DELIVERY_HMAC_SECRET")); simpleSecret != "" {
		if *keyID == "" {
			*keyID = "default"
		}
		return simpleSecret, nil
	}

	if *keyID == "" {
		return "", errors.New("gemster inbox: HMAC key ID is required when using GEMSTER_INBOX_HMAC_KEYS_JSON")
	}
	raw := strings.TrimSpace(os.Getenv("GEMSTER_INBOX_HMAC_KEYS_JSON"))
	keys := map[string]string{}
	if raw == "" || json.Unmarshal([]byte(raw), &keys) != nil {
		return "", errors.New("gemster inbox: GEMSTER_INBOX_HMAC_SECRET or GEMSTER_INBOX_HMAC_KEYS_JSON is required")
	}
	secret := keys[*keyID]
	if secret == "" {
		return "", fmt.Errorf("gemster inbox: no HMAC secret for key %q", *keyID)
	}
	return secret, nil
}

func (c *Client) Send(ctx context.Context, delivery outbounddelivery.Delivery) error {
	delivery.Recipient = strings.TrimSpace(delivery.Recipient)
	if delivery.ID == "" || delivery.TenantID == uuid.Nil || delivery.Recipient == "" ||
		delivery.SourceKind != outbounddelivery.SourceCron || delivery.SourceID == "" ||
		delivery.Title == "" || delivery.Body == "" || delivery.CreatedAt.IsZero() {
		return errors.New("gemster inbox: incomplete delivery")
	}

	body, err := json.Marshal(map[string]any{
		"version":        1,
		"deliveryId":     delivery.ID,
		"tenantId":       delivery.TenantID.String(),
		"recipientEmail": delivery.Recipient,
		"source": map[string]any{
			"kind":        delivery.SourceKind,
			"executionId": delivery.SourceID,
		},
		"title":     delivery.Title,
		"body":      delivery.Body,
		"createdAt": delivery.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("gemster inbox: marshal delivery: %w", err)
	}

	timestamp := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+deliveryPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("gemster inbox: build request: %w", err)
	}
	req.Header.Set(headerKeyID, c.keyID)
	req.Header.Set(headerTimestamp, strconv.FormatInt(timestamp, 10))
	req.Header.Set(headerSignature, "v1="+sign(c.secret, c.keyID, timestamp, body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gemster inbox: send delivery: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gemster inbox: receiver returned status %d", resp.StatusCode)
	}
	return nil
}

func sign(secret, keyID string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "v1.%s.%d.", keyID, timestamp)
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

package gemsterinbox

import (
	"errors"
	"fmt"
	"strings"
)

// RecipientFor validates the direct-user recipient required by Gemster Inbox.
func RecipientFor(userID, peerKind string) (string, error) {
	email := strings.TrimSpace(userID)
	if peerKind == "group" {
		return "", errors.New("gemster_inbox delivery requires a direct user context")
	}
	if !looksLikeEmail(email) {
		return "", fmt.Errorf("gemster_inbox delivery requires an email-shaped user ID (got %q)", email)
	}
	return email, nil
}

func looksLikeEmail(s string) bool {
	if s == "" || strings.Count(s, "@") != 1 {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return strings.Contains(s[at+1:], ".")
}

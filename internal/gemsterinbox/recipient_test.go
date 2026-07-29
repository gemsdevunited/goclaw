package gemsterinbox

import "testing"

func TestRecipientFor(t *testing.T) {
	tests := []struct {
		name     string
		userID   string
		peerKind string
		want     string
		wantErr  string
	}{
		{name: "trims direct email", userID: " user@example.com ", want: "user@example.com"},
		{name: "rejects group", userID: "group:telegram:-100123", peerKind: "group", wantErr: "direct user context"},
		{name: "rejects non-email", userID: "not-an-email", wantErr: "email-shaped user ID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RecipientFor(tt.userID, tt.peerKind)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("RecipientFor error: %v", err)
				}
				if got != tt.want {
					t.Fatalf("recipient = %q, want %q", got, tt.want)
				}
				return
			}
			if err == nil || !contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}

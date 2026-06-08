package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactStringCoversAccountIDSpellings(t *testing.T) {
	secret := "plain-account-id"
	raw := strings.Join([]string{
		`accountID=plain-account-id`,
		`account_id: plain-account-id`,
		`{"accountId":"plain-account-id"}`,
		`{"accountID":"plain-account-id"}`,
	}, "\n")
	got := RedactString(raw)
	if strings.Contains(got, secret) {
		t.Fatalf("redacted string leaked account id: %s", got)
	}
}

func TestSlogRedactsSensitiveAccountIDAttributes(t *testing.T) {
	var buf bytes.Buffer
	logger := New("info", &buf)
	logger.Info("submit", "account_id", "plain-account-id", "accountID", "other-account-id", "accountId", "third-account-id")
	got := buf.String()
	for _, secret := range []string{"plain-account-id", "other-account-id", "third-account-id"} {
		if strings.Contains(got, secret) {
			t.Fatalf("log leaked account id %q: %s", secret, got)
		}
	}
	if strings.Count(got, "[REDACTED]") < 3 {
		t.Fatalf("log did not redact account ids: %s", got)
	}
}

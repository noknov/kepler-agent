package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestVerifySignature(t *testing.T) {
	secret := "signing-secret"
	ts := "1000"
	body := `{"type":"event_callback"}`
	base := "v0:" + ts + ":" + body
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(base))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	if err := VerifySignature(secret, ts, body, sig, time.Unix(1000, 0)); err != nil {
		t.Fatalf("expected valid signature: %v", err)
	}
	if err := VerifySignature(secret, ts, body, sig+"bad", time.Unix(1000, 0)); err == nil {
		t.Fatal("expected invalid signature")
	}
}

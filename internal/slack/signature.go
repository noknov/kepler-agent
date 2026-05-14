package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func VerifySignature(secret, timestamp, body, signature string, now time.Time) error {
	if secret == "" {
		return fmt.Errorf("missing signing secret")
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid slack timestamp")
	}
	if delta := now.Sub(time.Unix(ts, 0)); delta > 5*time.Minute || delta < -5*time.Minute {
		return fmt.Errorf("slack timestamp outside replay window")
	}

	base := "v0:" + timestamp + ":" + body
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(base))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature))) {
		return fmt.Errorf("invalid slack signature")
	}
	return nil
}

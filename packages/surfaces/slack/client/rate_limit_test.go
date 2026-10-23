package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	slackconversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func TestRateLimitCooldownIsSharedByMethod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := make(chan string, 4)
		limited := true
		client := NewTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls <- req.URL.Path
			status, header := 200, make(http.Header)
			if limited {
				limited = false
				status = 429
				header.Set("Retry-After", "60")
			}
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(`{"ok":true,"ts":"1.0"}`)), Request: req}, nil
		}))
		// The first caller times out, but its workspace/method cooldown survives.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := client.StartStream(ctx, slackconversation.StreamStart{Channel: "C1", ThreadTS: "T1"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if path := <-calls; path != "/api/chat.startStream" {
			t.Fatal(path)
		}
		done := make(chan error, 1)
		go func() {
			_, err := client.StartStream(context.Background(), slackconversation.StreamStart{Channel: "C2", ThreadTS: "T2"})
			done <- err
		}()
		synctest.Wait()
		select {
		case path := <-calls:
			t.Fatalf("second conversation ignored Retry-After: %s", path)
		default:
		}
		// Stop is a different method and must remain immediately available.
		if err := client.StopStream(context.Background(), "C3", "1.0"); err != nil {
			t.Fatal(err)
		}
		if path := <-calls; path != "/api/chat.stopStream" {
			t.Fatalf("stop blocked by another method: %s", path)
		}
		time.Sleep(59 * time.Second)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if path := <-calls; path != "/api/chat.startStream" {
			t.Fatal(path)
		}
	})
}

func TestCooldownExtensionNeverShortensWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cooldown methodCooldowns
		cooldown.deferFor("method", 10*time.Second)
		cooldown.deferFor("method", time.Second)
		started := time.Now()
		if err := cooldown.wait(context.Background(), "method"); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed != 10*time.Second {
			t.Fatalf("wait = %v", elapsed)
		}
	})
}

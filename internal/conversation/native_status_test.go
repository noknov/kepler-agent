package conversation

import (
	"context"
	"testing"
	"time"

	"github.com/wati/oncall-agent/internal/agent"
)

func TestNativeThreadStatusRoutesStaticAndLoadingMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messenger := &nativeStatusMessenger{}
	status := newNativeThreadStatus(ctx, messenger, "C1", "100.000", agent.LocaleZH, func(err error) {
		t.Fatalf("unexpected status error: %v", err)
	})

	// updateStatic on a fresh instance should carry the default initial loading
	// message so Slack shows our fixed "正在思考..." instead of its own carousel.
	status.updateStatic()
	calls := waitForNativeStatusCalls(t, messenger, 1)
	if calls[0].status != "思考中" {
		t.Fatalf("static status = %q, want 思考中", calls[0].status)
	}
	if got, want := calls[0].loadingMessages, []string{"思考中..."}; !stringSlicesEqual(got, want) {
		t.Fatalf("initial static update messages = %#v, want %#v", got, want)
	}

	status.updateLoadingMessage("正在读取 block 工具结果")
	calls = waitForNativeStatusCalls(t, messenger, 2)
	if calls[1].status != "思考中" {
		t.Fatalf("loading update changed static status: %#v", calls[1])
	}
	if got, want := calls[1].loadingMessages, []string{"正在读取 block 工具结果"}; !stringSlicesEqual(got, want) {
		t.Fatalf("loading update messages = %#v, want %#v", got, want)
	}

	status.updateStatic()
	calls = waitForNativeStatusCalls(t, messenger, 3)
	if calls[2].status != "思考中" {
		t.Fatalf("static refresh changed static status: %#v", calls[2])
	}
	if got, want := calls[2].loadingMessages, []string{"正在读取 block 工具结果"}; !stringSlicesEqual(got, want) {
		t.Fatalf("static refresh should preserve last avatar loading message = %#v, want %#v", got, want)
	}
}

func waitForNativeStatusCalls(t *testing.T, messenger *nativeStatusMessenger, count int) []nativeStatusCall {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		calls := messenger.Calls()
		if len(calls) >= count {
			return calls
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("native status calls = %#v, want at least %d", messenger.Calls(), count)
	return nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

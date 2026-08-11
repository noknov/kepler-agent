package redisclient

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestIntegrationJSONQueueIsOrderedDeduplicatedAndDrainable(t *testing.T) {
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL is not set")
	}
	client, err := New(url)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	key := "test:agent-queue:" + time.Now().UTC().Format("20060102150405.000000000")
	defer client.Del(context.Background(), key+":data", key+":order")
	type item struct {
		Value string `json:"value"`
	}
	for _, entry := range []struct {
		id    string
		value string
	}{{"a", "first"}, {"a", "duplicate"}, {"b", "second"}} {
		if err := client.EnqueueJSON(context.Background(), key, entry.id, item{entry.value}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	values, err := client.DrainJSONQueue(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 {
		t.Fatalf("drain returned %d values, want 2", len(values))
	}
	for index, want := range []string{"first", "second"} {
		var got item
		if err := json.Unmarshal(values[index], &got); err != nil || got.Value != want {
			t.Fatalf("value %d = %+v, err=%v, want %q", index, got, err, want)
		}
	}
	again, err := client.DrainJSONQueue(context.Background(), key)
	if err != nil || len(again) != 0 {
		t.Fatalf("second drain = %q, err=%v", again, err)
	}
}

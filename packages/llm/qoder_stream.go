package llm

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// Qoder emits reasoning for long-running calls. A total HTTP timeout interrupts
// healthy streams; bound silence instead. The caller still owns the total deadline.
func qoderStreamRequest(client *http.Client, request *http.Request, handler StreamHandler) (Response, error) {
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	var expired atomic.Bool
	var timer *time.Timer
	timeout := client.Timeout
	if timeout > 0 {
		timer = time.AfterFunc(timeout, func() { expired.Store(true); cancel() })
		defer timer.Stop()
	}
	streamClient := *client
	streamClient.Timeout = 0
	response, err := streamClient.Do(request.Clone(ctx))
	if err != nil {
		if expired.Load() {
			return Response{}, context.DeadlineExceeded
		}
		return Response{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Response{}, NewProviderError("qoder", response.StatusCode, "model request failed")
	}
	result, err := readQoderStream(qoderActivityReader{Reader: response.Body, timer: timer, timeout: timeout}, handler)
	if expired.Load() {
		return Response{}, context.DeadlineExceeded
	}
	return result, err
}

type qoderActivityReader struct {
	io.Reader
	timer   *time.Timer
	timeout time.Duration
}

func (r qoderActivityReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 && r.timer != nil {
		r.timer.Reset(r.timeout)
	}
	return n, err
}

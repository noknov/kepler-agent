package conversation

import (
	"context"
	"log"
	"time"

	"github.com/noknov/slack-copilot-agent/internal/agent"
	"github.com/noknov/slack-copilot-agent/internal/llm"
)

const progressTaskID = "thinking"

type turnProgress struct {
	service *Service
	ctx     context.Context
	req     Request
	locale  string

	statusMessenger ThreadStatusMessenger
	useNativeStatus bool
	useStream       bool
	useProgress     bool

	nativeStatus *nativeThreadStatus
	answerStream *dmStreamWriter
	markdown     *streamMarkdownBuffer
	thinkingTS   string
	progressTS   string
	stopped      bool

	currentStatus          string
	displayedContextTokens int
	baseContextTokens      int
	lastUsageUpdate        time.Time
}

func newTurnProgress(ctx, runCtx context.Context, s *Service, req Request, locale string) *turnProgress {
	p := &turnProgress{
		service:       s,
		ctx:           ctx,
		req:           req,
		locale:        locale,
		currentStatus: agent.StepStatus(locale, 0),
	}
	statusMessenger, useNativeStatus := s.Messenger.(ThreadStatusMessenger)
	if isWebChannel(req.Channel) {
		useNativeStatus = false
	}
	p.statusMessenger = statusMessenger
	p.useNativeStatus = useNativeStatus
	p.useStream = useNativeStatus

	if !useNativeStatus {
		streamTS, err := s.Messenger.StartStream(ctx, req.Channel, req.ThreadTS, req.UserID)
		p.useProgress = err == nil && streamTS != ""
		p.useStream = p.useProgress
		p.progressTS = streamTS
		if err != nil {
			logStreamFallback(err)
		}
	}
	if !p.useStream {
		p.thinkingTS, _ = s.Messenger.PostMessage(ctx, req.Channel, req.ThreadTS, ":thinking_face: ...")
	}
	if p.useNativeStatus && statusMessenger != nil {
		p.nativeStatus = newNativeThreadStatus(runCtx, statusMessenger, req.Channel, req.ThreadTS, locale, func(err error) {
			s.recordDeliveryError(req, "", err)
		})
		go p.nativeStatus.keepAlive()
	}
	if p.useProgress {
		p.markdown = &streamMarkdownBuffer{
			ctx:     ctx,
			channel: req.Channel,
			append:  p.appendMarkdown,
			canFlush: func() bool {
				return !p.stopped && p.progressTS != ""
			},
		}
	}
	return p
}

func (p *turnProgress) cleanup() {
	if p == nil {
		return
	}
	if p.useNativeStatus {
		clearThreadStatus(context.Background(), p.statusMessenger, p.req.Channel, p.req.ThreadTS)
	}
	if p.useProgress && !p.stopped {
		_ = p.service.Messenger.StopStream(context.Background(), p.req.Channel, p.progressTS)
	} else if p.thinkingTS != "" {
		_ = p.service.Messenger.DeleteMessage(context.Background(), p.req.Channel, p.thinkingTS)
	}
	if p.markdown != nil {
		p.markdown.Close()
	}
}

func (p *turnProgress) UseStream() bool {
	return p != nil && p.useStream
}

func (p *turnProgress) UseNativeStatus() bool {
	return p != nil && p.useNativeStatus
}

func (p *turnProgress) Stopped() bool {
	return p == nil || p.stopped
}

func (p *turnProgress) AnswerStream() *dmStreamWriter {
	if p == nil {
		return nil
	}
	return p.answerStream
}

func (p *turnProgress) AnswerStreamOK(streamed bool) bool {
	return p != nil && streamed && p.answerStream != nil && !p.answerStream.Failed() && p.answerStream.TS() != ""
}

func (p *turnProgress) CloseAnswerStream() {
	if p != nil && p.answerStream != nil {
		p.answerStream.Close()
	}
}

func (p *turnProgress) CloseMarkdown() {
	if p != nil && p.markdown != nil {
		p.markdown.Close()
	}
}

func (p *turnProgress) SetBaseContextTokens(tokens int) {
	if p == nil {
		return
	}
	p.baseContextTokens = tokens
	if tokens > 0 {
		p.setCurrentUsage(tokens)
	}
}

func (p *turnProgress) UpdateLiveUsage(delta string) {
	if p == nil || !p.useProgress || delta == "" || p.baseContextTokens <= 0 {
		return
	}
	if time.Since(p.lastUsageUpdate) < 750*time.Millisecond {
		return
	}
	p.lastUsageUpdate = time.Now()
	if p.setCurrentUsage(p.baseContextTokens) {
		p.AppendTaskUpdate(p.currentStatus, "in_progress")
	}
}

func (p *turnProgress) UpdateAPIUsage(usage llm.Usage) {
	if p == nil {
		return
	}
	if !p.useProgress {
		_ = p.setCurrentUsage(contextTokensFromUsage(usage, p.baseContextTokens))
		return
	}
	if p.setCurrentUsage(contextTokensFromUsage(usage, p.baseContextTokens)) {
		p.AppendTaskUpdate(p.currentStatus, "in_progress")
	}
}

func (p *turnProgress) SetContextUsage(tokens int) {
	if p != nil {
		p.setCurrentUsage(tokens)
	}
}

func (p *turnProgress) AppendTaskUpdate(title, status string) {
	if p == nil {
		return
	}
	if title != "" {
		p.currentStatus = title
	}
	p.appendProgress([]map[string]any{
		{"type": "task_update", "id": progressTaskID, "title": p.currentStatus, "status": status},
	})
}

func (p *turnProgress) Stop() {
	if p == nil {
		return
	}
	if p.useNativeStatus {
		clearThreadStatus(context.Background(), p.statusMessenger, p.req.Channel, p.req.ThreadTS)
		p.stopped = true
		return
	}
	if p.stopped || p.progressTS == "" {
		return
	}
	p.stopped = true
	_ = p.service.Messenger.StopStream(context.Background(), p.req.Channel, p.progressTS)
}

func (p *turnProgress) StartAnswerStream() *dmStreamWriter {
	if p == nil {
		return nil
	}
	if p.answerStream != nil {
		return p.answerStream
	}
	p.CloseMarkdown()
	p.AppendTaskUpdate(agent.GeneratingStatus(p.locale), "in_progress")
	p.answerStream = &dmStreamWriter{
		ctx:       p.ctx,
		messenger: p.service.Messenger,
		channel:   p.req.Channel,
		threadTS:  p.req.ThreadTS,
		userID:    p.req.UserID,
	}
	return p.answerStream
}

func (p *turnProgress) WireRunner(runner *agent.Runner) {
	if p == nil || runner == nil {
		return
	}
	if p.useStream {
		runner.OnStream = func(ev agent.StreamEvent) {
			switch ev.Kind {
			case agent.StreamNarration:
				p.UpdateLiveUsage(ev.Delta)
			case agent.StreamAnswer:
				p.UpdateLiveUsage(ev.Delta)
				if stream := p.StartAnswerStream(); stream != nil {
					stream.Write(ev.Delta)
				}
			}
		}
	}
	runner.StatusUpdate = func(status string) {
		if p.nativeStatus != nil {
			p.nativeStatus.updateStatic()
			return
		}
		p.AppendTaskUpdate(status, "in_progress")
	}
	runner.LoadingMessageUpdate = func(status string) {
		if p.nativeStatus != nil {
			p.nativeStatus.updateLoadingMessage(status)
			return
		}
		p.AppendTaskUpdate(status, "in_progress")
	}
	runner.OnUsage = p.UpdateAPIUsage
	runner.OnLLMStepComplete = func() {
		p.AppendTaskUpdate(p.currentStatus, "in_progress")
	}
}

func (p *turnProgress) ProgressAppender() func([]map[string]any) {
	return func(chunks []map[string]any) {
		p.appendProgress(chunks)
	}
}

func (p *turnProgress) setCurrentUsage(used int) bool {
	if used <= 0 {
		return false
	}
	if p.displayedContextTokens > 0 && used < p.displayedContextTokens {
		return false
	}
	p.displayedContextTokens = used
	return true
}

func (p *turnProgress) appendProgress(chunks []map[string]any) {
	if p.useNativeStatus {
		for _, chunk := range chunks {
			if chunk["type"] != "task_update" {
				continue
			}
			status, _ := chunk["status"].(string)
			if status == "complete" {
				clearThreadStatus(context.Background(), p.statusMessenger, p.req.Channel, p.req.ThreadTS)
				continue
			}
			title, _ := chunk["title"].(string)
			if title != "" {
				p.currentStatus = title
			}
		}
		return
	}
	if !p.useStream || p.stopped {
		return
	}
	for _, chunk := range chunks {
		if chunk["type"] != "task_update" || p.displayedContextTokens <= 0 {
			continue
		}
		title, _ := chunk["title"].(string)
		if title == "" {
			title = p.currentStatus
		}
		chunk["title"] = streamingTaskTitle(title, p.displayedContextTokens, p.service.Memory.MaxContextTokens)
	}
	if err := p.service.Messenger.AppendStream(p.ctx, p.req.Channel, p.progressTS, chunks); err == nil {
		return
	} else if !isSlackStreamExpired(err) {
		p.service.recordDeliveryError(p.req, p.progressTS, err)
		return
	}
	if !p.restartStream() {
		return
	}
	if err := p.service.Messenger.AppendStream(p.ctx, p.req.Channel, p.progressTS, chunks); err != nil {
		p.service.recordDeliveryError(p.req, p.progressTS, err)
		p.stopped = true
	}
}

func (p *turnProgress) appendMarkdown(text string) error {
	if p.useNativeStatus || !p.useStream || p.stopped || text == "" {
		return nil
	}
	chunks := []map[string]any{{"type": "markdown_text", "text": text}}
	if err := p.service.Messenger.AppendStream(p.ctx, p.req.Channel, p.progressTS, chunks); err == nil {
		return nil
	} else if !isSlackStreamExpired(err) {
		p.service.recordDeliveryError(p.req, p.progressTS, err)
		p.stopped = true
		return err
	}
	if !p.restartStream() {
		return nil
	}
	if err := p.service.Messenger.AppendStream(p.ctx, p.req.Channel, p.progressTS, chunks); err != nil {
		p.service.recordDeliveryError(p.req, p.progressTS, err)
		p.stopped = true
		return err
	}
	return nil
}

func (p *turnProgress) restartStream() bool {
	newTS, err := p.service.Messenger.StartStream(p.ctx, p.req.Channel, p.req.ThreadTS, p.req.UserID)
	if err != nil || newTS == "" {
		if err != nil {
			p.service.recordDeliveryError(p.req, p.progressTS, err)
		}
		p.stopped = true
		return false
	}
	p.progressTS = newTS
	return true
}

func isWebChannel(channel string) bool {
	return len(channel) >= len("web:") && channel[:len("web:")] == "web:"
}

func logStreamFallback(err error) {
	if err != nil {
		// Kept as a small helper to avoid importing log from service.go only for
		// progress-stream setup after the extraction.
		log.Printf("stream fallback: %v", err)
	}
}

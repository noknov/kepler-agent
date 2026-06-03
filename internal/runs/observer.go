package runs

import (
	"context"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/observability"
)

type Observer struct {
	Store    Store
	Run      *Run
	Rates    observability.CostRates
	stepSeq  int
	started  time.Time
	finished bool
}

func NewObserver(store Store, run Run, rates observability.CostRates) *Observer {
	now := time.Now().UTC()
	if run.ID == "" {
		run.ID = NewID()
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	if run.Status == "" {
		run.Status = "running"
	}
	o := &Observer{Store: store, Run: &run, Rates: rates, started: run.StartedAt}
	o.save(context.Background())
	return o
}

func (o *Observer) LLMCall(usage llm.Usage, d time.Duration, err error) {
	cost := o.Rates.EstimateUSD(usage)
	o.Run.Usage.PromptTokens += usage.PromptTokens
	o.Run.Usage.CompletionTokens += usage.CompletionTokens
	o.Run.Usage.TotalTokens += usage.TotalTokens
	o.Run.Usage.CacheReadInputTokens += usage.CacheReadInputTokens
	o.Run.Usage.CacheCreationInputTokens += usage.CacheCreationInputTokens
	o.Run.Usage.ReasoningTokens += usage.ReasoningTokens
	o.Run.EstimatedCostUSD += cost
	o.appendStep(Step{
		Type:             "llm",
		Name:             o.Run.Model,
		DurationMS:       d.Milliseconds(),
		Usage:            usage,
		EstimatedCostUSD: cost,
		Error:            errorString(err),
	})
}

func (o *Observer) ToolCall(name string, d time.Duration, err error) {
	o.appendStep(Step{
		Type:       "tool",
		Name:       name,
		DurationMS: d.Milliseconds(),
		Error:      errorString(err),
	})
}

func (o *Observer) Finish(status, errorID string, err error, final string) {
	if o.finished {
		return
	}
	o.finished = true
	o.Run.Status = status
	o.Run.ErrorID = errorID
	o.Run.Error = errorString(err)
	if final != "" {
		o.Run.FinalHash = HashText(final)
	}
	o.Run.EndedAt = time.Now().UTC()
	o.Run.DurationMS = o.Run.EndedAt.Sub(o.Run.StartedAt).Milliseconds()
	o.Run.Quality = scoreRun(*o.Run)
	o.save(context.Background())
}

func (o *Observer) LinkSlackMessage(channel, messageTS string) {
	if o == nil || o.Run == nil || messageTS == "" {
		return
	}
	o.Run.SlackChannel = channel
	o.Run.SlackMessageTS = messageTS
	o.save(context.Background())
}

func (o *Observer) appendStep(step Step) {
	o.stepSeq++
	step.ID = o.Run.ID + "-step-" + time.Now().UTC().Format("150405.000000000")
	step.StartedAt = time.Now().UTC().Add(-time.Duration(step.DurationMS) * time.Millisecond)
	o.Run.Steps = append(o.Run.Steps, step)
	o.save(context.Background())
}

func (o *Observer) save(ctx context.Context) {
	if o.Store == nil || o.Run == nil {
		return
	}
	_ = o.Store.Save(ctx, *o.Run)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

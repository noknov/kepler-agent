package hosted

import (
	"context"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
)

type progressModelStub struct{}

func (progressModelStub) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	return model.Response{}, nil
}

func TestSelectProgressModelFallsBackToPrimary(t *testing.T) {
	primary := progressModelStub{}
	client, name := selectProgressModel(primary, "primary-model", nil, "")
	if client == nil || name != "primary-model" {
		t.Fatalf("progress model = %T %q, want primary", client, name)
	}
}

func TestSelectProgressModelPrefersSecondary(t *testing.T) {
	primary, secondary := progressModelStub{}, progressModelStub{}
	client, name := selectProgressModel(primary, "primary-model", secondary, "secondary-model")
	if client == nil || name != "secondary-model" {
		t.Fatalf("progress model = %T %q, want secondary", client, name)
	}
}

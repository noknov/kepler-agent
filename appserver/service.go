package appserver

import (
	"context"
	"io"

	protocolserver "github.com/noknov/slack-copilot-agent/packages/appserver"
	"github.com/noknov/slack-copilot-agent/packages/config"
	sharedlogging "github.com/noknov/slack-copilot-agent/packages/infra/logging"
	"github.com/noknov/slack-copilot-agent/packages/observability"
	"github.com/noknov/slack-copilot-agent/packages/platform"
	appruntime "github.com/noknov/slack-copilot-agent/packages/runtime"
)

func Run(ctx context.Context, in io.Reader, out io.Writer) error {
	cfg, err := config.LoadFor(config.ProfileAppServer)
	if err != nil {
		return err
	}
	sharedlogging.Configure(cfg.Observing.LogLevel)
	stores, err := platform.NewCoreStores(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	defer stores.Close()

	recorder := observability.NewRecorder()
	runtime := appruntime.NewAppServerAgentRuntime(cfg, recorder, stores.Redis, stores.UserPrefs)
	if runtime.Core != nil {
		runtime.Core.Events = nil
	}
	server := protocolserver.New(runtime.Core)
	server.Runs = stores.Runs
	server.EventStore = stores.Protocol
	server.Rates = runtime.CostRates
	server.Provider = cfg.LLM.Provider
	server.Model = cfg.LLM.Model
	server.SpillStore = stores.Runs
	return server.Serve(ctx, in, out)
}

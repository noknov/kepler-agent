package slacktool

import "github.com/noknov/slack-copilot-agent/packages/agent/tool"

const surfaceName = "slack"

// Surface-specific descriptor helpers stay in this package so packages/agent/tool
// remains transport-neutral. Generic tools under packages/tools/* declare only
// capability effects; BindSurface adds Slack visibility metadata at registration.

func surfaceDeps(extra ...string) []string {
	return append([]string{surfaceName}, extra...)
}

func readNetwork(deps ...string) []tool.DescriptorOption {
	return append(tool.ReadNetworkParallel(surfaceDeps(deps...)...), tool.WithSurfaces(surfaceName))
}

func externalWrite(deps ...string) []tool.DescriptorOption {
	return append(tool.ExternalWrite(surfaceDeps(deps...)...), tool.WithSurfaces(surfaceName))
}

func bindSurface(item tool.Tool, extraDeps ...string) tool.Tool {
	return tool.BindSurface(item, surfaceName, extraDeps...)
}

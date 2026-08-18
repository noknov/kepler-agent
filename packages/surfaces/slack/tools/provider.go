package slacktool

import (
	"context"
	"errors"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/connections"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

// ConnectedClientSource resolves a Slack API client for the caller's linked user token.
type ConnectedClientSource struct {
	Service connections.Service
}

func connectedSlackClient(ctx context.Context, service connections.Service, userID string) (*slack.Client, error) {
	if service.Store == nil {
		return nil, errors.New("slack connections are not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("user id is required for slack")
	}
	token, err := service.Store.Token(ctx, userID, connections.ProviderSlack)
	if err != nil {
		if errors.Is(err, connections.ErrNotConnected) {
			return nil, service.Required(userID, connections.ProviderSlack)
		}
		return nil, err
	}
	return slack.NewClient(token, ""), nil
}

func (s ConnectedClientSource) SlackClient(ctx context.Context, call tool.Call) (*slack.Client, error) {
	return connectedSlackClient(ctx, s.Service, call.Scope.UserID)
}

func beginConnectedClient(ctx context.Context, source ConnectedClientSource, call tool.Call) (*slack.Client, *tool.Result, error) {
	client, err := source.SlackClient(ctx, call)
	if err != nil {
		if result, convErr := connections.ToolResult(err); convErr == nil {
			return nil, &result, nil
		}
		return nil, nil, err
	}
	return client, nil, nil
}

// FileSearcher resolves Slack file APIs for a tool call.
type FileSearcherSource interface {
	FileSearcher(ctx context.Context, call tool.Call) (FileSearcher, error)
}

// BotFileSearcher uses the workspace bot token.
type BotFileSearcher struct{ Client *slack.Client }

func (s BotFileSearcher) FileSearcher(_ context.Context, _ tool.Call) (FileSearcher, error) {
	if s.Client == nil {
		return nil, errors.New("slack is not configured")
	}
	return s.Client, nil
}

// ConnectedFileSearcher uses the caller's linked Slack user token.
type ConnectedFileSearcher struct {
	Service connections.Service
}

func (s ConnectedFileSearcher) FileSearcher(ctx context.Context, call tool.Call) (FileSearcher, error) {
	return connectedSlackClient(ctx, s.Service, call.Scope.UserID)
}

func resolveFileSearcher(ctx context.Context, source FileSearcherSource, call tool.Call) (FileSearcher, error) {
	if source == nil {
		return nil, errors.New("slack file source is not configured")
	}
	return source.FileSearcher(ctx, call)
}

func beginFileSearch(ctx context.Context, source FileSearcherSource, fallback FileSearcher, call tool.Call) (FileSearcher, *tool.Result, error) {
	if source == nil {
		if fallback == nil {
			return nil, nil, errors.New("slack file search is not configured")
		}
		return fallback, nil, nil
	}
	searcher, err := resolveFileSearcher(ctx, source, call)
	if err != nil {
		if result, convErr := connections.ToolResult(err); convErr == nil {
			return nil, &result, nil
		}
		return nil, nil, err
	}
	return searcher, nil, nil
}

// PosterSource resolves Slack messaging APIs for a tool call.
type PosterSource interface {
	Poster(ctx context.Context, call tool.Call) (Poster, error)
}

// ConnectedPoster uses the caller's linked Slack user token.
type ConnectedPoster struct {
	Service connections.Service
}

func (s ConnectedPoster) Poster(ctx context.Context, call tool.Call) (Poster, error) {
	return connectedSlackClient(ctx, s.Service, call.Scope.UserID)
}

func beginUserPoster(ctx context.Context, source PosterSource, call tool.Call) (Poster, *tool.Result, error) {
	if source == nil {
		return nil, nil, errors.New("slack user messaging requires a connected Slack account")
	}
	poster, err := source.Poster(ctx, call)
	if err != nil {
		if result, convErr := connections.ToolResult(err); convErr == nil {
			return nil, &result, nil
		}
		return nil, nil, err
	}
	return poster, nil, nil
}

// ThreadReader loads Slack thread or conversation messages.
type ThreadReader interface {
	Replies(ctx context.Context, channel, threadTS string, limit int) ([]slack.Message, error)
	History(ctx context.Context, channel, latest string, limit int) ([]slack.Message, error)
}

// ConversationResolver resolves flexible Slack conversation references.
type ConversationResolver interface {
	ResolveReadTarget(ctx context.Context, in slack.ReadTargetInput) (slack.ReadTarget, error)
}

// ThreadReaderSource resolves Slack thread APIs for a tool call.
type ThreadReaderSource interface {
	ThreadReader(ctx context.Context, call tool.Call) (ThreadReader, error)
}

// ConnectedThreadReader uses the caller's linked Slack user token.
type ConnectedThreadReader struct {
	Service connections.Service
}

func (s ConnectedThreadReader) ThreadReader(ctx context.Context, call tool.Call) (ThreadReader, error) {
	return connectedSlackClient(ctx, s.Service, call.Scope.UserID)
}

// BotThreadReader uses the workspace bot token.
type BotThreadReader struct{ Client *slack.Client }

func (s BotThreadReader) ThreadReader(_ context.Context, _ tool.Call) (ThreadReader, error) {
	if s.Client == nil {
		return nil, errors.New("slack is not configured")
	}
	return s.Client, nil
}

// PreferConnectedThreadReader uses the caller's linked token when available and
// falls back to the bot token for the current conversation.
type PreferConnectedThreadReader struct {
	Connected ConnectedThreadReader
	Bot       BotThreadReader
}

func (s PreferConnectedThreadReader) ThreadReader(ctx context.Context, call tool.Call) (ThreadReader, error) {
	reader, err := s.Connected.ThreadReader(ctx, call)
	if err == nil {
		return reader, nil
	}
	if readRequiresUserConnection(call) {
		return nil, err
	}
	var required *connections.RequiredError
	if errors.As(err, &required) || errors.Is(err, connections.ErrNotConnected) {
		return s.Bot.ThreadReader(ctx, call)
	}
	return nil, err
}

func beginThreadRead(ctx context.Context, source ThreadReaderSource, call tool.Call) (ThreadReader, *tool.Result, error) {
	if source == nil {
		return nil, nil, errors.New("slack thread reads require a connected Slack account")
	}
	reader, err := source.ThreadReader(ctx, call)
	if err != nil {
		if result, convErr := connections.ToolResult(err); convErr == nil {
			return nil, &result, nil
		}
		return nil, nil, err
	}
	return reader, nil, nil
}

package slackbot

import (
	"context"
	"log"

	"github.com/noknov/slack-copilot-agent/packages/slack"
	"github.com/noknov/slack-copilot-agent/packages/slackhome"
)

func (s *Server) handleAppHome(ctx context.Context, ev slack.Event) {
	if ev.User == "" {
		return
	}
	if ev.Tab != "" && ev.Tab != "home" {
		return
	}
	if err := s.slack.PublishHome(ctx, ev.User, s.homeView(ev.User)); err != nil {
		log.Printf("publish home failed: %v", err)
	}
}

func (s *Server) homeView(userID string) map[string]any {
	if s.handler != nil {
		return s.handler.Home.View(userID)
	}
	return s.homeController().View(userID)
}

func (s *Server) homeController() slackhome.Controller {
	controller := slackhome.Controller{Cfg: s.cfg, Access: s.access, Slack: s.slack}
	if s.userPrefsStore != nil {
		controller.Store = s.userPrefsStore
	}
	return controller
}

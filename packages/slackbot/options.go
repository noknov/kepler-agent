package slackbot

type Options struct {
	SlackGateway  bool
	SlackWorker   bool
	Observability bool
	Background    bool
}

func AllInOneOptions() Options {
	return Options{
		SlackGateway:  true,
		SlackWorker:   true,
		Observability: true,
		Background:    true,
	}
}

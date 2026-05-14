package slack

type EventEnvelope struct {
	Type           string          `json:"type"`
	Challenge      string          `json:"challenge,omitempty"`
	EventID        string          `json:"event_id,omitempty"`
	EventTime      int64           `json:"event_time,omitempty"`
	Event          Event           `json:"event"`
	Authorizations []Authorization `json:"authorizations,omitempty"`
}

type Authorization struct {
	UserID string `json:"user_id"`
}

type Event struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype,omitempty"`
	User     string `json:"user,omitempty"`
	Text     string `json:"text,omitempty"`
	Channel  string `json:"channel,omitempty"`
	TS       string `json:"ts,omitempty"`
	ThreadTS string `json:"thread_ts,omitempty"`
	BotID    string `json:"bot_id,omitempty"`
	Reaction string `json:"reaction,omitempty"`
	Item     Item   `json:"item,omitempty"`
}

type Item struct {
	Type    string `json:"type"`
	Channel string `json:"channel,omitempty"`
	TS      string `json:"ts,omitempty"`
}

type Message struct {
	User     string `json:"user,omitempty"`
	BotID    string `json:"bot_id,omitempty"`
	Text     string `json:"text,omitempty"`
	TS       string `json:"ts,omitempty"`
	ThreadTS string `json:"thread_ts,omitempty"`
}

func (e Event) ConversationThreadTS() string {
	if e.ThreadTS != "" {
		return e.ThreadTS
	}
	return e.TS
}

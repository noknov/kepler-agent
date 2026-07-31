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
	Type        string `json:"type"`
	Subtype     string `json:"subtype,omitempty"`
	User        string `json:"user,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	Text        string `json:"text,omitempty"`
	Channel     string `json:"channel,omitempty"`
	ChannelType string `json:"channel_type,omitempty"`
	TS          string `json:"ts,omitempty"`
	ThreadTS    string `json:"thread_ts,omitempty"`
	BotID       string `json:"bot_id,omitempty"`
	Reaction    string `json:"reaction,omitempty"`
	Tab         string `json:"tab,omitempty"`
	Item        Item   `json:"item,omitempty"`
	Files       []File `json:"files,omitempty"`
	File        File   `json:"file,omitempty"`
	FileID      string `json:"file_id,omitempty"`
	ChannelID   string `json:"channel_id,omitempty"`
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
	Files    []File `json:"files,omitempty"`
}

type User struct {
	ID       string      `json:"id,omitempty"`
	Name     string      `json:"name,omitempty"`
	RealName string      `json:"real_name,omitempty"`
	Profile  UserProfile `json:"profile,omitempty"`
}

type UserProfile struct {
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	RealName    string `json:"real_name,omitempty"`
}

type File struct {
	ID                 string `json:"id,omitempty"`
	Name               string `json:"name,omitempty"`
	Title              string `json:"title,omitempty"`
	Mimetype           string `json:"mimetype,omitempty"`
	Filetype           string `json:"filetype,omitempty"`
	PrettyType         string `json:"pretty_type,omitempty"`
	Mode               string `json:"mode,omitempty"`
	Size               int64  `json:"size,omitempty"`
	URLPrivate         string `json:"url_private,omitempty"`
	URLPrivateDownload string `json:"url_private_download,omitempty"`
	Permalink          string `json:"permalink,omitempty"`
}

func (e Event) ConversationThreadTS() string {
	if e.ThreadTS != "" {
		return e.ThreadTS
	}
	return e.TS
}

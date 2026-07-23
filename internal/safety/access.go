package safety

type AccessPolicy struct {
	users    map[string]struct{}
	channels map[string]struct{}
}

func NewAccessPolicy(allowedUsers, allowedChannels []string) AccessPolicy {
	return AccessPolicy{
		users:    set(allowedUsers),
		channels: set(allowedChannels),
	}
}

func (p AccessPolicy) IsAllowed(userID, channelID string) bool {
	return p.AllowsUser(userID) && p.AllowsChannel(channelID)
}

func (p AccessPolicy) AllowsUser(userID string) bool {
	if len(p.users) == 0 {
		return true
	}
	_, ok := p.users[userID]
	return ok
}

func (p AccessPolicy) AllowsChannel(channelID string) bool {
	if len(p.channels) == 0 {
		return true
	}
	_, ok := p.channels[channelID]
	return ok
}

func set(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v != "" {
			m[v] = struct{}{}
		}
	}
	return m
}

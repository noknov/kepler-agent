package slackfiles

// ImageBudget limits image downloads across one or more Slack messages.
type ImageBudget struct {
	remainingCount int
	remainingBytes int
}

func MessageImageBudget() *ImageBudget {
	return &ImageBudget{remainingCount: MaxImageCount, remainingBytes: MaxImageTotalBytes}
}

func ThreadImageBudget() *ImageBudget {
	return &ImageBudget{remainingCount: MaxThreadHistoryImages, remainingBytes: MaxImageTotalBytes}
}

func (b *ImageBudget) take(count, bytes int) {
	if b == nil {
		return
	}
	b.remainingCount -= count
	b.remainingBytes -= bytes
}

func (b *ImageBudget) allow() (maxCount, maxBytes int) {
	if b == nil {
		return MaxImageCount, MaxImageTotalBytes
	}
	maxCount = b.remainingCount
	if maxCount > MaxImageCount {
		maxCount = MaxImageCount
	}
	maxBytes = b.remainingBytes
	if maxBytes > MaxImageBytes {
		maxBytes = MaxImageBytes
	}
	if maxCount <= 0 || maxBytes <= 0 {
		return 0, 0
	}
	return maxCount, maxBytes
}

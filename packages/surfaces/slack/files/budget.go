package slackfiles

// ImageBudget bounds the bytes downloaded for images in one request. It
// deliberately has no image-count limit: the model-facing token cost of an
// image is a small constant independent of the encoded file size, so the byte
// budget is the only bound that protects memory and bandwidth.
type ImageBudget struct {
	remainingBytes int
}

func MessageImageBudget() *ImageBudget {
	return &ImageBudget{remainingBytes: MaxImageTotalBytes}
}

func ThreadImageBudget() *ImageBudget {
	return &ImageBudget{remainingBytes: MaxImageTotalBytes}
}

func (b *ImageBudget) take(bytes int) {
	if b == nil {
		return
	}
	b.remainingBytes -= bytes
}

// allow returns the byte budget available for the next download, capped at the
// per-image limit.
func (b *ImageBudget) allow() int {
	if b == nil {
		return MaxImageBytes
	}
	if b.remainingBytes <= 0 {
		return 0
	}
	if b.remainingBytes > MaxImageBytes {
		return MaxImageBytes
	}
	return b.remainingBytes
}

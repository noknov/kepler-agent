package slackfiles

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

const (
	MaxAttachedFiles   = 20
	MaxImageCount      = 4
	MaxImageBytes      = 8 << 20
	MaxImageTotalBytes = 16 << 20
	MaxPDFBytes        = 16 << 20
	MaxPDFTextChars    = slack.DefaultMaxPDFExtractChars
)

type Downloader interface {
	DownloadFile(ctx context.Context, file slack.File, maxBytes int64) ([]byte, error)
}

func Append(text string, files []slack.File) string {
	filesText := slack.FormatFiles(files)
	if filesText == "" {
		return strings.TrimSpace(text)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return filesText
	}
	return text + "\n\n" + filesText
}

func Attach(ctx context.Context, client Downloader, text string, files []slack.File) (string, []llm.ContentPart) {
	omitted := 0
	if len(files) > MaxAttachedFiles {
		omitted = len(files) - MaxAttachedFiles
		files = files[:MaxAttachedFiles]
	}
	text = Append(text, files)
	if omitted > 0 {
		text = strings.TrimSpace(text) + fmt.Sprintf("\n\n[%d additional Slack files omitted; attachment limit is %d]", omitted, MaxAttachedFiles)
	}
	if excerpt := PDFExcerpts(ctx, client, files); excerpt != "" {
		text = strings.TrimSpace(text)
		if text == "" {
			text = excerpt
		} else {
			text += "\n\n" + excerpt
		}
	}
	return text, ImageParts(ctx, client, files)
}

func PDFExcerpts(ctx context.Context, client Downloader, files []slack.File) string {
	if client == nil {
		return ""
	}
	blocks := make([]string, 0, len(files))
	for _, file := range files {
		if !slack.IsPDFFile(file) {
			continue
		}
		if file.Size > MaxPDFBytes {
			log.Printf("skip slack pdf %s: size %d exceeds limit %d", file.ID, file.Size, MaxPDFBytes)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[PDF too large to extract; max "+FormatBytes(MaxPDFBytes)+"]"))
			continue
		}
		data, err := client.DownloadFile(ctx, file, MaxPDFBytes)
		if err != nil {
			log.Printf("skip slack pdf %s: download failed: %v", file.ID, err)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[Could not download PDF from Slack: "+err.Error()+"]"))
			continue
		}
		if !slack.IsPDFData(data) {
			log.Printf("skip slack pdf %s: downloaded content is not a PDF", file.ID)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[Downloaded file is not a valid PDF]"))
			continue
		}
		text, err := slack.ExtractPDFText(data, MaxPDFTextChars)
		if err != nil {
			log.Printf("skip slack pdf %s: extract failed: %v", file.ID, err)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[Could not extract text from PDF; it may be scanned/image-only. Ask the user to paste key details or send a screenshot.]"))
			continue
		}
		blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), text))
	}
	return strings.Join(blocks, "\n\n")
}

func ImageParts(ctx context.Context, client Downloader, files []slack.File) []llm.ContentPart {
	return ImagePartsWithBudget(ctx, client, files, MessageImageBudget())
}

func ImagePartsWithBudget(ctx context.Context, client Downloader, files []slack.File, budget *ImageBudget) []llm.ContentPart {
	if client == nil {
		return nil
	}
	if budget == nil {
		budget = MessageImageBudget()
	}
	parts := make([]llm.ContentPart, 0, len(files))
	for _, file := range files {
		maxCount, maxBytes := budget.allow()
		if maxCount <= 0 || maxBytes <= 0 {
			break
		}
		mime := NormalizedImageMIME(file)
		if mime == "" {
			continue
		}
		if file.Size > 0 && file.Size > MaxImageBytes {
			log.Printf("skip slack image %s: size %d exceeds limit %d", file.ID, file.Size, MaxImageBytes)
			continue
		}
		downloadLimit := MaxImageBytes
		if maxBytes < downloadLimit {
			downloadLimit = maxBytes
		}
		data, err := client.DownloadFile(ctx, file, int64(downloadLimit))
		if err != nil {
			log.Printf("skip slack image %s: %v", file.ID, err)
			continue
		}
		actualMIME := SniffImageMIME(data)
		if actualMIME == "" {
			log.Printf("skip slack image %s: downloaded content is not a supported image", file.ID)
			continue
		}
		if actualMIME != mime {
			log.Printf("slack image %s declared %s but detected %s", file.ID, mime, actualMIME)
		}
		dataURL := "data:" + actualMIME + ";base64," + base64.StdEncoding.EncodeToString(data)
		parts = append(parts, llm.ImageURLPart(dataURL))
		budget.take(1, len(data))
	}
	return parts
}

func NormalizedImageMIME(file slack.File) string {
	mime := strings.ToLower(strings.TrimSpace(file.Mimetype))
	switch mime {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return mime
	}
	switch strings.ToLower(strings.TrimSpace(file.Filetype)) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return ""
	}
}

func SniffImageMIME(data []byte) string {
	if len(data) >= 8 &&
		data[0] == 0x89 &&
		data[1] == 'P' &&
		data[2] == 'N' &&
		data[3] == 'G' &&
		data[4] == '\r' &&
		data[5] == '\n' &&
		data[6] == 0x1a &&
		data[7] == '\n' {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 12 &&
		string(data[0:4]) == "RIFF" &&
		string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(data) >= 6 && (string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a") {
		return "image/gif"
	}
	return ""
}

func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/slack"
)

func appendSlackFiles(text string, files []slack.File) string {
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

func (s *Server) attachSlackFiles(ctx context.Context, text string, files []slack.File) (string, []llm.ContentPart) {
	text = appendSlackFiles(text, files)
	if excerpt := s.slackPDFExcerpts(ctx, files); excerpt != "" {
		text = strings.TrimSpace(text)
		if text == "" {
			text = excerpt
		} else {
			text += "\n\n" + excerpt
		}
	}
	if excerpt := s.slackTextExcerpts(ctx, files); excerpt != "" {
		text = strings.TrimSpace(text)
		if text == "" {
			text = excerpt
		} else {
			text += "\n\n" + excerpt
		}
	}
	return text, s.slackImageParts(ctx, files)
}

func (s *Server) slackPDFExcerpts(ctx context.Context, files []slack.File) string {
	blocks := make([]string, 0, len(files))
	for _, file := range files {
		if !slack.IsPDFFile(file) {
			continue
		}
		if file.Size > maxSlackPDFBytes {
			log.Printf("skip slack pdf %s: size %d exceeds limit %d", file.ID, file.Size, maxSlackPDFBytes)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[PDF too large to extract; max "+formatBytes(maxSlackPDFBytes)+"]"))
			continue
		}
		data, err := s.slack.DownloadFile(ctx, file, maxSlackPDFBytes)
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
		text, err := slack.ExtractPDFText(data, maxSlackPDFTextChars)
		if err != nil {
			log.Printf("skip slack pdf %s: extract failed: %v", file.ID, err)
			blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), "[Could not extract text from PDF; it may be scanned/image-only. Ask the user to paste key details or send a screenshot.]"))
			continue
		}
		blocks = append(blocks, slack.FormatPDFExcerpt(slack.FileDisplayName(file), text))
	}
	return strings.Join(blocks, "\n\n")
}

func (s *Server) slackTextExcerpts(ctx context.Context, files []slack.File) string {
	blocks := make([]string, 0, len(files))
	for _, file := range files {
		if !shouldAttemptSlackTextExcerpt(file) {
			continue
		}
		declaredText := slack.IsTextFile(file)
		if file.Size > maxSlackTextBytes {
			log.Printf("skip slack text %s: size %d exceeds limit %d", file.ID, file.Size, maxSlackTextBytes)
			if declaredText {
				blocks = append(blocks, slack.FormatTextExcerpt(slack.FileDisplayName(file), "[Text file too large to read; max "+formatBytes(maxSlackTextBytes)+"]"))
			}
			continue
		}
		data, err := s.slack.DownloadFile(ctx, file, maxSlackTextBytes)
		if err != nil {
			log.Printf("skip slack text %s: download failed: %v", file.ID, err)
			if declaredText {
				blocks = append(blocks, slack.FormatTextExcerpt(slack.FileDisplayName(file), "[Could not download text file from Slack: "+err.Error()+"]"))
			}
			continue
		}
		text, err := slack.ExtractTextFile(data, maxSlackTextChars)
		if err != nil {
			log.Printf("skip slack text %s: extract failed: %v", file.ID, err)
			if declaredText {
				blocks = append(blocks, slack.FormatTextExcerpt(slack.FileDisplayName(file), "[Could not read text file: "+err.Error()+"]"))
			}
			continue
		}
		blocks = append(blocks, slack.FormatTextExcerpt(slack.FileDisplayName(file), text))
	}
	return strings.Join(blocks, "\n\n")
}

func shouldAttemptSlackTextExcerpt(file slack.File) bool {
	return !slack.IsPDFFile(file) && normalizedImageMIME(file) == ""
}

func formatBytes(n int64) string {
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

func (s *Server) slackImageParts(ctx context.Context, files []slack.File) []llm.ContentPart {
	parts := make([]llm.ContentPart, 0, len(files))
	for _, file := range files {
		mime := normalizedImageMIME(file)
		if mime == "" {
			continue
		}
		if file.Size > maxSlackImageBytes {
			log.Printf("skip slack image %s: size %d exceeds limit %d", file.ID, file.Size, maxSlackImageBytes)
			continue
		}
		data, err := s.slack.DownloadFile(ctx, file, maxSlackImageBytes)
		if err != nil {
			log.Printf("skip slack image %s: %v", file.ID, err)
			continue
		}
		actualMIME := sniffImageMIME(data)
		if actualMIME == "" {
			log.Printf("skip slack image %s: downloaded content is not a supported image", file.ID)
			continue
		}
		if actualMIME != mime {
			log.Printf("slack image %s declared %s but detected %s", file.ID, mime, actualMIME)
		}
		dataURL := "data:" + actualMIME + ";base64," + base64.StdEncoding.EncodeToString(data)
		parts = append(parts, llm.ImageURLPart(dataURL))
	}
	return parts
}

const (
	maxSlackImageBytes   = 8 << 20
	maxSlackPDFBytes     = 16 << 20
	maxSlackPDFTextChars = slack.DefaultMaxPDFExtractChars
	maxSlackTextBytes    = 16 << 20
	maxSlackTextChars    = slack.DefaultMaxTextExtractChars
)

func normalizedImageMIME(file slack.File) string {
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

func sniffImageMIME(data []byte) string {
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

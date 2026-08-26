package safety

import "strings"

// StreamRedactor applies Redactor only to complete logical records before they
// cross a streaming boundary. Ordinary records are lines; PEM private keys are
// one record from their BEGIN line through their END line. This deliberately
// trades sub-line latency for an exact equivalence with Redactor.Sanitize: a
// secret split between arbitrary transport deltas is never emitted first and
// corrected later.
type StreamRedactor struct {
	redactor Redactor
	pending  string
	pemBlock string
}

func NewStreamRedactor(redactor Redactor) *StreamRedactor {
	return &StreamRedactor{redactor: redactor}
}

// Append returns text safe to send immediately. It retains the unfinished
// line, because every single-line secret rule may legally continue until its
// line terminator.
func (r *StreamRedactor) Append(delta string) string {
	if r == nil || delta == "" {
		return delta
	}
	r.pending += delta

	var output strings.Builder
	for {
		lineEnd := strings.IndexByte(r.pending, '\n')
		if lineEnd < 0 {
			return output.String()
		}
		line := r.pending[:lineEnd+1]
		r.pending = r.pending[lineEnd+1:]

		if r.pemBlock != "" {
			r.pemBlock += line
			if isPrivateKeyEnd(line) {
				output.WriteString(r.redactor.Sanitize(r.pemBlock))
				r.pemBlock = ""
			}
			continue
		}
		if isPrivateKeyBegin(line) {
			r.pemBlock = line
			continue
		}
		output.WriteString(r.redactor.Sanitize(line))
	}
}

// Flush returns the final logical record, including an unterminated line or
// PEM block. Completion therefore cannot bypass the exact same sanitizer used
// for already streamed records.
func (r *StreamRedactor) Flush() string {
	if r == nil {
		return ""
	}
	pending := r.pemBlock + r.pending
	r.pemBlock = ""
	r.pending = ""
	return r.redactor.Sanitize(pending)
}

func isPrivateKeyBegin(line string) bool {
	upper := strings.ToUpper(line)
	return strings.Contains(upper, "-----BEGIN ") && strings.Contains(upper, " PRIVATE KEY-----")
}

func isPrivateKeyEnd(line string) bool {
	upper := strings.ToUpper(line)
	return strings.Contains(upper, "-----END ") && strings.Contains(upper, " PRIVATE KEY-----")
}

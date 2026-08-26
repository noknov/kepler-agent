package safety

import "testing"

func TestStreamRedactorMatchesBatchRedactionForEverySingleSplit(t *testing.T) {
	input := "before\n" +
		"SLACK_TOKEN=xoxb-secret-token-value\n" +
		"Authorization: Bearer xoxb-another-token\n" +
		"-----BEGIN PRIVATE KEY-----\nprivate-key-body\n-----END PRIVATE KEY-----\n" +
		"after\n"
	want := (Redactor{}).Sanitize(input)

	for split := 0; split <= len(input); split++ {
		redactor := NewStreamRedactor(Redactor{})
		got := redactor.Append(input[:split]) + redactor.Append(input[split:]) + redactor.Flush()
		if got != want {
			t.Fatalf("split %d produced %q, want %q", split, got, want)
		}
	}
}

func TestStreamRedactorMatchesBatchRedactionForByteChunks(t *testing.T) {
	input := "safe\nAuthorization: Bearer xoxb-final-token\nnext\n"
	redactor := NewStreamRedactor(Redactor{})
	var got string
	for index := range input {
		got += redactor.Append(input[index : index+1])
	}
	got += redactor.Flush()

	want := (Redactor{}).Sanitize(input)
	if got != want {
		t.Fatalf("byte chunks produced %q, want %q", got, want)
	}
}

func TestStreamRedactorDoesNotEmitUnterminatedSecretBeforeFlush(t *testing.T) {
	redactor := NewStreamRedactor(Redactor{})
	if got := redactor.Append("Authorization: Bearer xoxb-final-token"); got != "" {
		t.Fatalf("unexpected early output: %q", got)
	}
	if got := redactor.Flush(); got != "Authorization: Bearer [redacted]" {
		t.Fatalf("final stream value was not redacted: %q", got)
	}
}

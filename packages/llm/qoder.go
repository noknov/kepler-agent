package llm

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const QoderBaseURL = "https://api2.qoder.sh"

const qoderOpenAPI = "https://openapi.qoder.sh"
const qoderVersion = "1.1.68"
const qoderChatPath = "/algo/api/v2/service/pro/sse/agent_chat_generation"
const qoderChatQuery = "?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
const qoderCatalogTTL = 5 * time.Minute

// Qoder's native COSY protocol, independently adapted from its CLI protocol and
// the MIT-licensed pi-provider-qoder implementation. This public server key is
// protocol material, not an operator secret. See docs/models.md for compatibility.
const qoderPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// QoderClient performs exactly one model request. Kepler owns tool execution,
// retries, conversation state and compaction; no Qoder agent process is started.
// PATs are supplied by deploy secrets. Short-lived credentials remain in memory.
type QoderClient struct {
	baseURL, pat, machineID string
	httpClient              *http.Client
	mu                      sync.Mutex
	auth                    qoderAuth
	models                  []QoderModel
	catalogExpires          time.Time
}

type qoderAuth struct {
	token, uid, name, email, organization string
	expires                               time.Time
}

type QoderModel struct {
	Key     string          `json:"key"`
	Name    string          `json:"display_name"`
	Enabled bool            `json:"enable"`
	Vision  bool            `json:"is_vl"`
	Config  json.RawMessage `json:"-"`
}

func NewQoderClient(baseURL, pat string, timeout time.Duration) (*QoderClient, error) {
	if strings.TrimSpace(pat) == "" {
		return nil, fmt.Errorf("QODER_PERSONAL_ACCESS_TOKEN is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL != QoderBaseURL && baseURL != "https://api3.qoder.sh" {
		return nil, fmt.Errorf("qoder requires an official global model endpoint")
	}
	id, err := qoderUUID()
	if err != nil {
		return nil, err
	}
	return &QoderClient{baseURL: baseURL, pat: pat, machineID: id,
		httpClient: &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// SetTransport configures the HTTP transport before the client is used.
func (c *QoderClient) SetTransport(transport http.RoundTripper) {
	c.httpClient.Transport = transport
}

func (c *QoderClient) authenticate(ctx context.Context) (qoderAuth, error) {
	// Both credential and catalog initialization are serialized. The network
	// timeout and caller context bound initialization; no credentials hit disk.
	if err := ctx.Err(); err != nil {
		return qoderAuth{}, err
	}
	if c.auth.token != "" && time.Now().Add(time.Minute).Before(c.auth.expires) {
		return c.auth, nil
	}
	var token struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	body, err := json.Marshal(map[string]string{"personal_token": c.pat})
	if err != nil {
		return qoderAuth{}, err
	}
	if err := c.jsonRequest(ctx, http.MethodPost, qoderOpenAPI+"/api/v1/jobToken/exchange", body, nil, &token); err != nil {
		return qoderAuth{}, err
	}
	if token.Token == "" || !token.ExpiresAt.After(time.Now()) {
		return qoderAuth{}, fmt.Errorf("qoder returned invalid job-token credentials")
	}
	var user struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Email        string `json:"email"`
		Organization string `json:"organization_id"`
	}
	if err := c.jsonRequest(ctx, http.MethodGet, qoderOpenAPI+"/api/v1/userinfo", nil, http.Header{"Authorization": {"Bearer " + token.Token}}, &user); err != nil {
		return qoderAuth{}, err
	}
	if user.ID == "" {
		return qoderAuth{}, fmt.Errorf("qoder returned no account identity")
	}
	c.auth = qoderAuth{token: token.Token, uid: user.ID, name: user.Name, email: user.Email, organization: user.Organization, expires: token.ExpiresAt}
	return c.auth, nil
}

// Models returns the enabled account catalog, never a guessed fallback list.
func (c *QoderClient) Models(ctx context.Context) ([]QoderModel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	auth, err := c.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	if !time.Now().Before(c.catalogExpires) {
		endpoint := c.baseURL + "/algo/api/v2/model/list?Encode=1"
		headers, err := c.sign(endpoint, nil, auth)
		if err != nil {
			return nil, err
		}
		var catalog struct {
			Assistant []json.RawMessage `json:"assistant"`
		}
		if err := c.jsonRequest(ctx, http.MethodGet, endpoint, nil, headers, &catalog); err != nil {
			return nil, err
		}
		models := make([]QoderModel, 0, len(catalog.Assistant))
		for _, raw := range catalog.Assistant {
			var model QoderModel
			if err := json.Unmarshal(raw, &model); err != nil {
				return nil, fmt.Errorf("decode qoder model: %w", err)
			}
			if model.Enabled && model.Key != "" {
				model.Config = raw
				models = append(models, model)
			}
		}
		if len(models) == 0 {
			return nil, fmt.Errorf("qoder returned no enabled assistant models")
		}
		c.models, c.catalogExpires = models, time.Now().Add(qoderCatalogTTL)
	}
	result := make([]QoderModel, len(c.models))
	for i, m := range c.models {
		result[i] = m
		result[i].Config = bytes.Clone(m.Config)
	}
	return result, nil
}

func (c *QoderClient) jsonRequest(ctx context.Context, method, endpoint string, body []byte, headers http.Header, target any) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Auth APIs may echo credential material. Do not log their bodies.
		return NewProviderError("qoder", resp.StatusCode, "account or catalog request failed")
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(target)
}

func (c *QoderClient) Chat(ctx context.Context, req Request) (Response, error) {
	return c.ChatStream(ctx, req, StreamHandler{})
}

func (c *QoderClient) ChatStream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	models, err := c.Models(ctx)
	if err != nil {
		return Response{}, err
	}
	var selected *QoderModel
	for i := range models {
		if models[i].Key == req.Model || models[i].Name == req.Model {
			selected = &models[i]
			break
		}
	}
	if selected == nil {
		return Response{}, fmt.Errorf("qoder model %q is not in the enabled account catalog", req.Model)
	}
	c.mu.Lock()
	auth, err := c.authenticate(ctx)
	c.mu.Unlock()
	if err != nil {
		return Response{}, err
	}
	payload, err := c.requestBody(req, *selected)
	if err != nil {
		return Response{}, err
	}
	encoded := qoderEncode(payload)
	endpoint := c.baseURL + qoderChatPath + qoderChatQuery
	headers, err := c.sign(endpoint, encoded, auth)
	if err != nil {
		return Response{}, err
	}
	headers.Set("Accept", "text/event-stream")
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Model-Key", selected.Key)
	var config struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(selected.Config, &config); err != nil {
		return Response{}, err
	}
	headers.Set("X-Model-Source", config.Source)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header = headers
	return qoderStreamRequest(c.httpClient, httpReq, h)
}

func (c *QoderClient) requestBody(req Request, selected QoderModel) ([]byte, error) {
	if req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "none" {
		return nil, fmt.Errorf("unsupported qoder tool choice %q", req.ToolChoice)
	}
	id := req.Metadata["request_id"]
	if id == "" {
		var err error
		id, err = qoderUUID()
		if err != nil {
			return nil, err
		}
	}
	session := req.Metadata["session_id"]
	if session == "" {
		session = id
	}
	parameters := map[string]any{}
	if req.MaxTokens > 0 {
		parameters["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		parameters["temperature"] = *req.Temperature
	}
	if req.Thinking != "" {
		switch req.Thinking {
		case "none", "disabled", "off":
			parameters["enable_thinking"] = false
		case "enabled":
			parameters["enable_thinking"] = true
		default:
			parameters["enable_thinking"] = true
			parameters["reasoning_effort"] = req.Thinking
		}
	}
	messages := chatMessagesWithToolImages(req.Messages)
	for i := range messages {
		// The native gateway drops empty assistant content, orphaning its tool
		// results. A blank content value retains a structured tool-only turn.
		if messages[i].Role == "assistant" && messages[i].Content == "" && len(messages[i].ContentParts) == 0 && len(messages[i].ToolCalls) > 0 {
			messages[i].Content = " "
		}
	}
	tools := req.Tools
	if tools == nil || req.ToolChoice == "none" {
		tools = []ToolSpec{}
	}
	body := map[string]any{
		"request_id": id, "request_set_id": id, "chat_record_id": id, "session_id": session,
		"stream": true, "chat_task": "FREE_INPUT", "is_reply": true, "is_retry": false,
		"source": 1, "version": "3", "session_type": "qodercli", "agent_id": "agent_common", "task_id": "common",
		"system": "", "messages": messages, "tools": tools, "parameters": parameters, "model_config": selected.Config,
		"business": map[string]any{"product": "cli", "version": "1.0.0", "type": "agent", "stage": "start", "id": id, "name": "Kepler", "begin_at": time.Now().UnixMilli()},
	}
	return json.Marshal(body)
}

var qoderEncoding = base64.NewEncoding("_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!").WithPadding('$')

func qoderEncode(body []byte) []byte {
	encoded := qoderEncoding.AppendEncode(nil, body)
	third := len(encoded) / 3
	out := make([]byte, 0, len(encoded))
	out = append(out, encoded[len(encoded)-third:]...)
	out = append(out, encoded[third:len(encoded)-third]...)
	return append(out, encoded[:third]...)
}

func qoderUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (c *QoderClient) sign(endpoint string, body []byte, auth qoderAuth) (http.Header, error) {
	id, err := qoderUUID()
	if err != nil {
		return nil, err
	}
	// COSY decrypts the RSA result as a UTF-8 AES key, hence 16 ASCII bytes.
	key := []byte(strings.ReplaceAll(id, "-", "")[:16])
	info, err := json.Marshal(map[string]string{"uid": auth.uid, "security_oauth_token": auth.token, "name": auth.name, "email": auth.email, "aid": ""})
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padding := aes.BlockSize - len(info)%aes.BlockSize
	info = append(info, bytes.Repeat([]byte{byte(padding)}, padding)...)
	encrypted := make([]byte, len(info))
	cipher.NewCBCEncrypter(block, key).CryptBlocks(encrypted, info)
	pemBlock, _ := pem.Decode([]byte(qoderPublicKey))
	parsed, err := x509.ParsePKIXPublicKey(pemBlock.Bytes)
	if err != nil {
		return nil, err
	}
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, parsed.(*rsa.PublicKey), key)
	if err != nil {
		return nil, err
	}
	cosyKey := base64.StdEncoding.EncodeToString(wrapped)
	payload, err := json.Marshal(struct {
		Version     string `json:"version"`
		RequestID   string `json:"requestId"`
		Info        string `json:"info"`
		CosyVersion string `json:"cosyVersion"`
		IDEVersion  string `json:"ideVersion"`
	}{"v1", id, base64.StdEncoding.EncodeToString(encrypted), qoderVersion, ""})
	if err != nil {
		return nil, err
	}
	payloadB64 := base64.StdEncoding.EncodeToString(payload)
	date := strconv.FormatInt(time.Now().Unix(), 10)
	uri, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	// MD5 is mandated by COSY; it is not a password or general-purpose digest.
	digest := md5.New()
	_, _ = io.WriteString(digest, payloadB64+"\n"+cosyKey+"\n"+date+"\n")
	_, _ = digest.Write(body)
	_, _ = io.WriteString(digest, "\n"+strings.TrimPrefix(uri.Path, "/algo"))
	h := make(http.Header)
	h.Set("Authorization", "Bearer COSY."+payloadB64+"."+hex.EncodeToString(digest.Sum(nil)))
	h.Set("Cosy-Key", cosyKey)
	h.Set("Cosy-Date", date)
	h.Set("Cosy-User", auth.uid)
	h.Set("Cosy-Version", qoderVersion)
	h.Set("Cosy-Machineid", c.machineID)
	h.Set("Cosy-Machinetoken", c.machineID)
	h.Set("Cosy-Machinetype", "5")
	h.Set("Cosy-Clienttype", "5")
	h.Set("Cosy-Data-Policy", "disagree")
	h.Set("Login-Version", "v2")
	h.Set("Cosy-Scene", "assistant")
	h.Set("Cosy-Business-Product", "cli")
	h.Set("Cosy-Business-Type", "agent")
	if auth.organization != "" {
		h.Set("Cosy-Organization-Id", auth.organization)
	}
	return h, nil
}

func readQoderStream(r io.Reader, h StreamHandler) (Response, error) {
	result := Response{Message: Message{Role: "assistant"}, Streamed: true}
	calls := map[int]*ToolCall{}
	var streamErr error
	done, terminal, firstTokenSeen := false, false, false
	err := readSSE(r, func(ev sseEvent) bool {
		if ev.Data == "[DONE]" {
			done = true
			return false
		}
		var envelope struct {
			Status int             `json:"statusCodeValue"`
			Body   json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &envelope); err != nil {
			streamErr = fmt.Errorf("decode qoder stream envelope: %w", err)
			return false
		}
		if ev.Event == "error" || (envelope.Status != 0 && envelope.Status != 200) {
			status := envelope.Status
			if status == 0 {
				status = 502
			}
			streamErr = NewProviderError("qoder", status, "native stream returned an error")
			return false
		}
		data := envelope.Body
		if len(data) == 0 {
			streamErr = fmt.Errorf("qoder stream is missing its body")
			return false
		}
		if data[0] == '"' {
			var text string
			if err := json.Unmarshal(data, &text); err != nil {
				streamErr = err
				return false
			}
			if text == "[DONE]" {
				done = true
				return false
			}
			data = []byte(text)
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Code    json.RawMessage `json:"code"`
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
					Calls     []struct {
						Index    int          `json:"index"`
						ID       string       `json:"id"`
						Function ToolFunction `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				Finish string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			streamErr = fmt.Errorf("decode qoder stream chunk: %w", err)
			return false
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			streamErr = NewProviderError("qoder", 502, "model returned an error")
			return false
		}
		if len(chunk.Code) > 0 && string(chunk.Code) != "0" && string(chunk.Code) != `"0"` && string(chunk.Code) != "null" {
			status := 502
			if string(chunk.Code) == `"105"` || string(chunk.Code) == "105" {
				status = 401
			}
			streamErr = NewProviderError("qoder", status, "model returned a native error code")
			return false
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			var usage openAIUsage
			if err := json.Unmarshal(chunk.Usage, &usage); err != nil {
				streamErr = fmt.Errorf("decode qoder usage: %w", err)
				return false
			}
			if usage.Reported {
				result.Usage = usage.toUsage()
				if h.OnUsage != nil {
					h.OnUsage(result.Usage)
				}
			}
			// Native credits stay in metadata, never in token or USD counters.
			result.Raw, _ = json.Marshal(map[string]json.RawMessage{"usage": chunk.Usage})
		}
		if len(chunk.Choices) == 0 {
			return true
		}
		choice := chunk.Choices[0]
		if !firstTokenSeen && (choice.Delta.Content != "" || choice.Delta.Reasoning != "" || len(choice.Delta.Calls) > 0) {
			firstTokenSeen = true
			if h.OnFirstToken != nil {
				h.OnFirstToken()
			}
		}
		if choice.Delta.Content != "" {
			result.Message.Content += choice.Delta.Content
			if h.OnText != nil {
				h.OnText(TextDelta{Text: choice.Delta.Content})
			}
		}
		result.Message.ReasoningContent += choice.Delta.Reasoning
		for _, delta := range choice.Delta.Calls {
			if delta.Index < 0 || delta.Index > 1023 {
				streamErr = fmt.Errorf("qoder returned an invalid tool index")
				return false
			}
			if len(calls) == 0 && h.OnToolCallsStarted != nil {
				h.OnToolCallsStarted()
			}
			call := calls[delta.Index]
			if call == nil {
				call = &ToolCall{Type: "function"}
				calls[delta.Index] = call
			}
			if delta.ID != "" {
				if call.ID != "" && call.ID != delta.ID {
					streamErr = fmt.Errorf("qoder changed an active tool call ID")
					return false
				}
				call.ID = delta.ID
			}
			if delta.Function.Name != "" {
				call.Function.Name = delta.Function.Name
			}
			call.Function.Arguments += delta.Function.Arguments
		}
		if choice.Finish != "" {
			terminal = true
			result.FinishReason = choice.Finish
		}
		return true
	})
	if streamErr != nil {
		return Response{}, streamErr
	}
	if err != nil {
		return Response{}, err
	}
	if !done || !terminal {
		return Response{}, ProtocolError{Provider: "qoder", Detail: fmt.Sprintf("stream ended before completed inference (done=%t terminal=%t finish=%q calls=%d)", done, terminal, result.FinishReason, len(calls))}
	}
	indices := make([]int, 0, len(calls))
	for i := range calls {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		call := *calls[i]
		var args map[string]json.RawMessage
		validArguments := json.Unmarshal([]byte(call.Function.Arguments), &args) == nil && args != nil
		if call.ID == "" || call.Function.Name == "" || !validArguments {
			return Response{}, ProtocolError{Provider: "qoder", Detail: fmt.Sprintf("incomplete tool call (index=%d id_present=%t name_present=%t argument_bytes=%d arguments_object=%t finish=%q done=%t output_tokens=%d reasoning_tokens=%d)", i, call.ID != "", call.Function.Name != "", len(call.Function.Arguments), validArguments, result.FinishReason, done, result.Usage.CompletionTokens, result.Usage.ReasoningTokens)}
		}
		result.Message.ToolCalls = append(result.Message.ToolCalls, call)
	}
	if len(result.Message.ToolCalls) == 0 && strings.TrimSpace(result.Message.Content) == "" {
		return Response{}, EmptyResponseError{Provider: "qoder", StopReason: result.FinishReason, Retryable: true}
	}
	if result.FinishReason == "tool_calls" && len(result.Message.ToolCalls) == 0 {
		return Response{}, fmt.Errorf("qoder completed tool inference without structured tool calls")
	}
	if h.OnToolCallComplete != nil {
		for _, call := range result.Message.ToolCalls {
			h.OnToolCallComplete(call)
		}
	}
	return result, nil
}

var _ StreamClient = (*QoderClient)(nil)

// Package decisiondrex implements the ports.Decision adapter for the
// Drex/Jev System One decision API.
//
// Only the standard library plus core/domain and core/ports are used.
package decisiondrex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
)

// Profile selects the wire-compatible decision API endpoint family.
type Profile string

// Supported profiles.
const (
	// ProfileDrex talks to the Drex cloud API.
	ProfileDrex Profile = "drex"
	// ProfileJev talks to the Jev (TypeSafe) wire-compatible API.
	ProfileJev Profile = "jev"
)

// Default endpoint parts per profile.
const (
	defaultDrexBase = "https://console.nace.ai"
	drexPath        = "/v1/systemone"
	defaultJevBase  = "https://api.typesafe.ai/v1"
	jevPath         = "/systemone"
)

// defaultTimeout applies when neither the request nor the config sets one.
const defaultTimeout = 30 * time.Second

// maxBodySize caps a single response body at 1 MiB.
const maxBodySize = 1 << 20

// Default input-token prices per 1B tokens, as decimal strings.
const (
	defaultDrexPrice = "60"
	defaultJevPrice  = "42"
)

// billion is the divisor converting a per-1B-token price to a per-token cost.
const billion = "1000000000"

// costScale is the Decimal scale used for costUsd.
const costScale = 12

// Config tunes the decision-drex provider.
type Config struct {
	// Profile selects drex or jev defaults for base URL, path and price.
	Profile Profile
	// BaseURL overrides the profile default base; the profile path is appended.
	BaseURL string
	// SecretRef is the secret reference resolved via Deps.Secrets.
	SecretRef string
	// PricePerBillion is the input-token price per 1B tokens, decimal string.
	PricePerBillion string
	// Timeout is the default request deadline; 0 means 30s.
	Timeout time.Duration
}

// Provider is a ports.Decision implementation for Drex/Jev System One.
type Provider struct {
	cfg      Config
	deps     ports.Deps
	endpoint string
	price    domain.Decimal
	client   *http.Client
}

// New builds a Provider from cfg and host deps.
func New(cfg Config, deps ports.Deps) (*Provider, error) {
	var base, path, defaultPrice string
	switch cfg.Profile {
	case ProfileDrex:
		base, path, defaultPrice = defaultDrexBase, drexPath, defaultDrexPrice
	case ProfileJev:
		base, path, defaultPrice = defaultJevBase, jevPath, defaultJevPrice
	default:
		return nil, domain.NewError(domain.CodeInvalidRequest,
			"decision-drex: unknown profile")
	}
	if strings.TrimSpace(cfg.SecretRef) == "" {
		return nil, domain.NewError(domain.CodeInvalidRequest,
			"decision-drex: empty secret reference")
	}
	priceStr := cfg.PricePerBillion
	if priceStr == "" {
		priceStr = defaultPrice
	}
	price, err := domain.ParseDecimal(priceStr)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInvalidRequest,
			"decision-drex: invalid price per billion", err)
	}
	if strings.TrimSpace(cfg.BaseURL) != "" {
		base = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	return &Provider{
		cfg:      Config{Profile: cfg.Profile, BaseURL: cfg.BaseURL, SecretRef: cfg.SecretRef, PricePerBillion: priceStr, Timeout: timeout},
		deps:     deps,
		endpoint: base + path,
		price:    price,
		client:   cloneClient(deps.HTTPClient),
	}, nil
}

// cloneClient copies base (or builds a default client) and disables redirects.
func cloneClient(base *http.Client) *http.Client {
	var c *http.Client
	if base == nil {
		c = &http.Client{}
	} else {
		cp := *base
		c = &cp
	}
	c.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return c
}

// Descriptor advertises the provider identity and capabilities.
func (p *Provider) Descriptor() ports.Descriptor {
	return ports.Descriptor{
		ID:           "decision-drex",
		Name:         "Drex/Jev decision",
		Kind:         ports.KindDecision,
		Version:      "0.1.0-m0",
		Capabilities: []string{"choice", "noul", "score"},
		ConfigFields: []ports.ConfigField{
			{Name: "profile", Type: "string", Description: "API profile: drex or jev", Required: true},
			{Name: "baseUrl", Type: "url", Description: "Override for the profile base URL"},
			{Name: "secretRef", Type: "string", Description: "Secret reference for the API key", Required: true, Secret: true},
			{Name: "pricePerBillion", Type: "string", Description: "Input-token price per 1B tokens, decimal string"},
		},
		Degradations: []string{},
	}
}

// Decide sends one advisory request and validates the answer set.
func (p *Provider) Decide(ctx context.Context, req domain.DecisionRequest) (domain.DecisionResponse, error) {
	timeout := p.cfg.Timeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	secret := ""
	if p.deps.Secrets == nil {
		return domain.DecisionResponse{}, domain.NewError(domain.CodeAuth,
			"decision-drex: secret store is not configured")
	}
	var err error
	secret, err = p.deps.Secrets.Get(ctx, p.cfg.SecretRef)
	if err != nil || secret == "" {
		return domain.DecisionResponse{}, domain.WrapError(domain.CodeAuth,
			"decision-drex: cannot resolve API secret", errNoSecret{})
	}

	body, err := buildRequestBody(req)
	if err != nil {
		return domain.DecisionResponse{}, err
	}

	var cancel context.CancelFunc = func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return domain.DecisionResponse{}, domain.WrapError(domain.CodeInvalidRequest,
			"decision-drex: cannot build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+secret)
	httpReq.Header.Set("Accept", "application/json")

	call := *p.client
	if timeout > 0 {
		call.Timeout = timeout
	}
	resp, err := call.Do(httpReq)
	if err != nil {
		return domain.DecisionResponse{}, mapTransportError(ctx, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return domain.DecisionResponse{}, mapTransportError(ctx, err)
	}
	if len(raw) > maxBodySize {
		return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
			"decision-drex: response body exceeds 1 MiB")
	}

	if resp.StatusCode != http.StatusOK {
		return domain.DecisionResponse{}, mapStatusError(resp.StatusCode, resp.Header.Get("Retry-After"), raw)
	}
	return parseResponse(raw, req, p.price)
}

// errNoSecret hides the secret-store failure detail from the message chain.
type errNoSecret struct{}

func (errNoSecret) Error() string { return "secret unavailable" }

// mapTransportError classifies a failed HTTP round trip.
func mapTransportError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		e := domain.WrapError(domain.CodeTimeout, "decision-drex: request canceled or timed out", err)
		e.Retryable = true
		return e
	}
	if ctx != nil && ctx.Err() != nil {
		e := domain.WrapError(domain.CodeTimeout, "decision-drex: request canceled or timed out", err)
		e.Retryable = true
		return e
	}
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Timeout() {
		e := domain.WrapError(domain.CodeTimeout, "decision-drex: request timed out", err)
		e.Retryable = true
		return e
	}
	e := domain.WrapError(domain.CodeUnavailable, "decision-drex: transport error", err)
	e.Retryable = true
	return e
}

// vendorErrorEnvelope carries the vendor error code, if present.
type vendorErrorEnvelope struct {
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// mapStatusError maps an HTTP failure status to a domain error.
// Only the status and the vendor error code enter the message: headers,
// the full body and the secret never do.
func mapStatusError(status int, retryAfter string, raw []byte) error {
	code := ""
	var env vendorErrorEnvelope
	if json.Unmarshal(raw, &env) == nil && env.Error != nil {
		code = strings.TrimSpace(env.Error.Code)
	}
	suffix := ""
	if code != "" {
		suffix = " (" + code + ")"
	}
	msg := func(s string) string { return "decision-drex: " + s + suffix }
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return domain.NewError(domain.CodeAuth, msg("authentication failed with status "+strconv.Itoa(status)))
	case http.StatusPaymentRequired:
		return domain.NewError(domain.CodePaymentRequired, msg("payment required with status 402"))
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge:
		return domain.NewError(domain.CodeInvalidRequest, msg("invalid request with status "+strconv.Itoa(status)))
	case http.StatusTooManyRequests:
		m := "rate limited with status 429"
		if secs, ok := parseRetryAfter(retryAfter); ok {
			m += ", retry after " + strconv.FormatInt(secs, 10) + "s"
		}
		e := domain.NewError(domain.CodeRateLimit, msg(m))
		e.Retryable = true
		return e
	case http.StatusInternalServerError, http.StatusServiceUnavailable, 529:
		e := domain.NewError(domain.CodeUnavailable, msg("provider unavailable with status "+strconv.Itoa(status)))
		e.Retryable = true
		return e
	default:
		if status >= 400 && status < 500 {
			return domain.NewError(domain.CodeInvalidRequest, msg("request failed with status "+strconv.Itoa(status)))
		}
		e := domain.NewError(domain.CodeUnavailable, msg("provider failed with status "+strconv.Itoa(status)))
		e.Retryable = true
		return e
	}
}

// parseRetryAfter parses a Retry-After header as delay seconds.
func parseRetryAfter(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
		return n, true
	}
	return 0, false
}

// buildRequestBody encodes the wire JSON manually to preserve the option
// order: top-level keys are model, state and questions; per question the
// criteria hold an object (key -> description) for choice and an array of
// descriptions for score. There is no options field on the wire.
func buildRequestBody(req domain.DecisionRequest) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	buf.WriteString(`"model":`)
	writeJSONString(&buf, req.Model)
	buf.WriteString(`,"state":`)
	state := []byte("{}")
	if req.State != nil {
		var err error
		state, err = json.Marshal(req.State)
		if err != nil {
			return nil, domain.WrapError(domain.CodeInvalidRequest,
				"decision-drex: cannot encode state", err)
		}
	}
	buf.Write(state)
	buf.WriteString(`,"questions":{`)
	for i, q := range req.Questions {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeJSONString(&buf, q.ID)
		buf.WriteByte(':')
		buf.WriteString(`{"type":`)
		writeJSONString(&buf, string(q.Type))
		buf.WriteString(`,"instructions":`)
		writeJSONString(&buf, q.Instructions)
		switch q.Type {
		case domain.QuestionChoice:
			buf.WriteString(`,"criteria":{`)
			for j, o := range q.Options {
				if j > 0 {
					buf.WriteByte(',')
				}
				writeJSONString(&buf, o.Key)
				buf.WriteByte(':')
				writeJSONString(&buf, o.Description)
			}
			buf.WriteByte('}')
		case domain.QuestionScore:
			buf.WriteString(`,"criteria":[`)
			for j, o := range q.Options {
				if j > 0 {
					buf.WriteByte(',')
				}
				writeJSONString(&buf, o.Description)
			}
			buf.WriteByte(']')
		case domain.QuestionNoul:
			// No criteria on the wire for noul.
		default:
			return nil, domain.NewError(domain.CodeInvalidRequest,
				"decision-drex: unknown question type")
		}
		buf.WriteByte('}')
	}
	buf.WriteString(`}}`)
	return buf.Bytes(), nil
}

// writeJSONString appends the JSON-quoted string s to buf.
func writeJSONString(buf *bytes.Buffer, s string) {
	b, err := json.Marshal(s)
	if err != nil {
		buf.WriteString(`""`)
		return
	}
	buf.Write(b)
}

// wireUsage mirrors the vendor usage block with snake_case fields.
type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// wireAnswer mirrors one vendor answer object.
type wireAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Legend        []string           `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// wireResponse mirrors the vendor decision response envelope.
type wireResponse struct {
	Model            string                `json:"model"`
	Answers          map[string]wireAnswer `json:"answers"`
	Usage            wireUsage             `json:"usage"`
	RequestID        string                `json:"request_id"`
	EvaluationTimeMS *float64              `json:"evaluation_time_ms"`
	LatencyMS        *float64              `json:"latency_ms"`
}

// parseResponse decodes, validates and converts a vendor response body.
func parseResponse(raw []byte, req domain.DecisionRequest, price domain.Decimal) (domain.DecisionResponse, error) {
	var wr wireResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return domain.DecisionResponse{}, domain.WrapError(domain.CodeInvalidOutput,
			"decision-drex: invalid response JSON", err)
	}
	if strings.TrimSpace(wr.Model) == "" {
		return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
			"decision-drex: response model is empty")
	}
	byID := make(map[string]domain.Question, len(req.Questions))
	for _, q := range req.Questions {
		byID[q.ID] = q
	}
	if len(wr.Answers) != len(req.Questions) {
		return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
			"decision-drex: answer count does not match question count")
	}
	answers := make(map[string]domain.Answer, len(req.Questions))
	for id, wa := range wr.Answers {
		q, ok := byID[id]
		if !ok {
			return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: answer for unknown question")
		}
		if wa.Type != string(q.Type) {
			return domain.DecisionResponse{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: answer type does not match question type")
		}
		a, err := convertAnswer(q, wa)
		if err != nil {
			return domain.DecisionResponse{}, err
		}
		answers[id] = a
	}
	cost, err := costUSD(wr.Usage.InputTokens, price)
	if err != nil {
		return domain.DecisionResponse{}, domain.WrapError(domain.CodeInvalidOutput,
			"decision-drex: cannot compute cost", err)
	}
	var latency int64
	switch {
	case wr.EvaluationTimeMS != nil:
		latency = int64(math.Round(*wr.EvaluationTimeMS))
	case wr.LatencyMS != nil:
		latency = int64(math.Round(*wr.LatencyMS))
	}
	out := domain.DecisionResponse{
		Answers:        answers,
		Model:          wr.Model,
		RequestedModel: req.Model,
		Usage: domain.Usage{
			InputTokens:  wr.Usage.InputTokens,
			OutputTokens: wr.Usage.OutputTokens,
			CostUSD:      cost,
			LatencyMS:    latency,
		},
		RequestID: wr.RequestID,
		Raw:       append(json.RawMessage(nil), raw...),
	}
	return out, nil
}

// convertAnswer validates one answer against its question.
func convertAnswer(q domain.Question, wa wireAnswer) (domain.Answer, error) {
	a := domain.Answer{Type: q.Type}
	switch q.Type {
	case domain.QuestionNoul:
		if wa.Noul == nil || math.IsNaN(*wa.Noul) || math.IsInf(*wa.Noul, 0) ||
			*wa.Noul < 0 || *wa.Noul > 1 {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: noul out of range [0, 1]")
		}
		v := *wa.Noul
		a.Noul = &v
		if wa.Confidence != nil {
			if math.IsNaN(*wa.Confidence) || math.IsInf(*wa.Confidence, 0) ||
				*wa.Confidence < 0 || *wa.Confidence > 1 {
				return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
					"decision-drex: noul confidence out of range [0, 1]")
			}
			c := *wa.Confidence
			a.Confidence = &c
		}
		return a, nil
	case domain.QuestionScore:
		if wa.Score == nil || math.IsNaN(*wa.Score) || math.IsInf(*wa.Score, 0) {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: score is missing or not finite")
		}
		if len(wa.Legend) == 0 {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: score legend is empty")
		}
		if len(wa.Probabilities) == 0 {
			return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
				"decision-drex: score probabilities are empty")
		}
		s := *wa.Score
		a.Score = &s
		a.Legend = append([]string(nil), wa.Legend...)
		a.Probabilities = copyProbs(wa.Probabilities)
		if wa.Confidence != nil {
			if math.IsNaN(*wa.Confidence) || math.IsInf(*wa.Confidence, 0) {
				return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
					"decision-drex: score confidence is not finite")
			}
			c := *wa.Confidence
			a.Confidence = &c
		}
		return a, nil
	case domain.QuestionChoice:
		return convertChoice(q, wa)
	default:
		return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput,
			"decision-drex: unknown question type")
	}
}

// convertChoice validates a choice answer: exact key match, finite [0,1]
// values, a unit sum, argmax choice and a matching confidence.
func convertChoice(q domain.Question, wa wireAnswer) (domain.Answer, error) {
	invalid := func(msg string) (domain.Answer, error) {
		return domain.Answer{}, domain.NewError(domain.CodeInvalidOutput, "decision-drex: "+msg)
	}
	n := len(q.Options)
	if n < 2 {
		return invalid("choice needs at least two options")
	}
	if len(wa.Probabilities) != n {
		return invalid("choice probability count does not match options")
	}
	max := math.Inf(-1)
	sum := 0.0
	for _, o := range q.Options {
		p, ok := wa.Probabilities[o.Key]
		if !ok {
			return invalid("choice probabilities miss an option")
		}
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return invalid("choice probability out of range [0, 1]")
		}
		sum += p
		if p > max {
			max = p
		}
	}
	if math.Abs(sum-1) > 0.001 {
		return invalid("choice probabilities do not sum to 1")
	}
	got, ok := wa.Probabilities[wa.Choice]
	if !ok || got != max {
		return invalid("choice is not the argmax")
	}
	if wa.Confidence == nil {
		return invalid("choice confidence is missing")
	}
	if math.IsNaN(*wa.Confidence) || math.IsInf(*wa.Confidence, 0) {
		return invalid("choice confidence is not finite")
	}
	want := (max - 1/float64(n)) / (1 - 1/float64(n))
	if math.Abs(*wa.Confidence-want) > 0.02 {
		return invalid("choice confidence does not match recomputed value")
	}
	a := domain.Answer{
		Type:          domain.QuestionChoice,
		Choice:        wa.Choice,
		Probabilities: copyProbs(wa.Probabilities),
	}
	c := *wa.Confidence
	a.Confidence = &c
	return a, nil
}

// copyProbs duplicates a probability map.
func copyProbs(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// costUSD computes inputTokens * pricePerBillion / 1e9 rounded to 12 places.
func costUSD(inputTokens int, price domain.Decimal) (domain.Decimal, error) {
	tokens, err := domain.ParseDecimal(strconv.Itoa(inputTokens))
	if err != nil {
		return domain.Decimal{}, err
	}
	bn, err := domain.ParseDecimal(billion)
	if err != nil {
		return domain.Decimal{}, err
	}
	return tokens.Mul(price).QuoRound(bn, costScale)
}

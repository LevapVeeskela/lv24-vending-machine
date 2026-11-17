package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxRequestBytes caps one rehearsal request body.
const maxRequestBytes = 1 << 20

// klineIntervals are the synthetic candle grids served by the stub.
var klineIntervals = map[string]time.Duration{
	"1m": time.Minute,
	"5m": 5 * time.Minute,
	"1h": time.Hour,
}

// gateChoiceAnswers are the gate-v1 answers that produce an ALLOW under the
// default policy while still satisfying decision-drex choice validation.
var gateChoiceAnswers = map[string]decisionAnswer{
	"data_quality": {
		Type:          "choice",
		Choice:        "ok",
		Probabilities: map[string]float64{"ok": 0.9, "insufficient": 0.1},
		Confidence:    f64(0.8),
	},
	"signal_alignment": {
		Type:          "choice",
		Choice:        "aligned",
		Probabilities: map[string]float64{"aligned": 0.8, "neutral": 0.15, "conflict": 0.05},
		Confidence:    f64(0.7),
	},
	"market_regime": {
		Type:          "choice",
		Choice:        "favorable",
		Probabilities: map[string]float64{"favorable": 0.7, "neutral": 0.25, "adverse": 0.05},
		Confidence:    f64(0.55),
	},
	"risk_check": {
		Type:          "choice",
		Choice:        "pass",
		Probabilities: map[string]float64{"pass": 0.9, "block": 0.1},
		Confidence:    f64(0.8),
	},
}

// decisionRequest is the subset of the vendor request the stub reads.
type decisionRequest struct {
	Model     string `json:"model"`
	Questions map[string]struct {
		Type     string          `json:"type"`
		Criteria json.RawMessage `json:"criteria"`
	} `json:"questions"`
}

// decisionAnswer mirrors one vendor answer object.
type decisionAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Legend        []string           `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// decisionResponse mirrors the vendor decision response envelope.
type decisionResponse struct {
	Model     string                    `json:"model"`
	Answers   map[string]decisionAnswer `json:"answers"`
	Usage     decisionUsage             `json:"usage"`
	RequestID string                    `json:"request_id"`
	EvalMS    float64                   `json:"evaluation_time_ms"`
}

// decisionUsage mirrors the vendor usage block.
type decisionUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// stub is one loopback rehearsal server. The zero value is not usable.
type stub struct {
	mu             sync.Mutex
	decisionStatus int
	telegramStatus int
	now            func() time.Time
	logger         *log.Logger

	// Request counters served by GET /ctl/stats. They count role requests,
	// never token or message content.
	decisionRequests int
	telegramRequests int
	klineRequests    map[string]int
}

// stats is the GET /ctl/stats payload: request counters by role.
type stats struct {
	Decision int            `json:"decision"`
	Telegram int            `json:"telegram"`
	Klines   map[string]int `json:"klines"`
}

// newStub builds the rehearsal handler with both roles at HTTP 200.
func newStub(now func() time.Time, logger *log.Logger) *stub {
	if now == nil {
		now = time.Now
	}
	return &stub{
		decisionStatus: http.StatusOK,
		telegramStatus: http.StatusOK,
		now:            now,
		logger:         logger,
		klineRequests:  make(map[string]int),
	}
}

// ServeHTTP routes the four rehearsal roles.
func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/systemone":
		s.decision(w, r)
	case r.URL.Path == "/api/v3/klines":
		s.klines(w, r)
	case r.URL.Path == "/ctl/stats":
		s.stats(w, r)
	case r.URL.Path == "/ctl":
		s.ctl(w, r)
	case strings.HasPrefix(r.URL.Path, "/bot") && strings.HasSuffix(r.URL.Path, "/sendMessage"):
		s.telegram(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// decision answers one decision-drex POST from the parsed request questions.
func (s *stub) decision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.countDecision()
	if status := s.roleStatus(false); status != http.StatusOK {
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": "smokestub_decision_status"}})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot read request"})
		return
	}
	var req decisionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request JSON"})
		return
	}
	answers, err := buildAnswers(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "smokestub-model"
	}
	writeJSON(w, http.StatusOK, decisionResponse{
		Model:     model,
		Answers:   answers,
		Usage:     decisionUsage{InputTokens: 42, OutputTokens: 7},
		RequestID: "smokestub-request",
		EvalMS:    1,
	})
}

// buildAnswers produces one valid answer per request question. The four
// gate-v1 questions get an ALLOW-friendly fixed answer; any other question is
// answered generically by type.
func buildAnswers(req decisionRequest) (map[string]decisionAnswer, error) {
	if len(req.Questions) == 0 {
		return nil, fmt.Errorf("request has no questions")
	}
	out := make(map[string]decisionAnswer, len(req.Questions))
	for id, q := range req.Questions {
		switch q.Type {
		case "choice":
			var criteria map[string]string
			if err := json.Unmarshal(q.Criteria, &criteria); err != nil {
				return nil, fmt.Errorf("question %q: invalid choice criteria", id)
			}
			if fixed, ok := gateChoiceAnswers[id]; ok && sameChoiceKeys(fixed.Probabilities, criteria) {
				out[id] = fixed
				continue
			}
			a, err := genericChoice(criteria)
			if err != nil {
				return nil, fmt.Errorf("question %q: %w", id, err)
			}
			out[id] = a
		case "noul":
			out[id] = decisionAnswer{Type: "noul", Noul: f64(0.9)}
		case "score":
			var criteria []string
			if err := json.Unmarshal(q.Criteria, &criteria); err != nil {
				return nil, fmt.Errorf("question %q: invalid score criteria", id)
			}
			a, err := genericScore(criteria)
			if err != nil {
				return nil, fmt.Errorf("question %q: %w", id, err)
			}
			out[id] = a
		default:
			return nil, fmt.Errorf("question %q: unknown type %q", id, q.Type)
		}
	}
	return out, nil
}

// sameChoiceKeys reports whether probs covers exactly the criteria keys.
func sameChoiceKeys(probs map[string]float64, criteria map[string]string) bool {
	if len(probs) != len(criteria) || len(probs) < 2 {
		return false
	}
	for key := range probs {
		if _, ok := criteria[key]; !ok {
			return false
		}
	}
	return true
}

// genericChoice picks the first sorted key and spreads the remaining mass.
func genericChoice(criteria map[string]string) (decisionAnswer, error) {
	keys := make([]string, 0, len(criteria))
	for key := range criteria {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) < 2 {
		return decisionAnswer{}, fmt.Errorf("choice needs at least two options")
	}
	const chosen = 0.8
	rest := (1 - chosen) / float64(len(keys)-1)
	probs := make(map[string]float64, len(keys))
	for i, key := range keys {
		if i == 0 {
			probs[key] = chosen
			continue
		}
		probs[key] = rest
	}
	n := float64(len(keys))
	confidence := (chosen - 1/n) / (1 - 1/n)
	return decisionAnswer{
		Type:          "choice",
		Choice:        keys[0],
		Probabilities: probs,
		Confidence:    f64(confidence),
	}, nil
}

// genericScore returns a uniform score answer over the criteria descriptions.
func genericScore(criteria []string) (decisionAnswer, error) {
	if len(criteria) == 0 {
		return decisionAnswer{}, fmt.Errorf("score needs at least one option")
	}
	p := 1 / float64(len(criteria))
	probs := make(map[string]float64, len(criteria))
	for i := range criteria {
		probs[strconv.Itoa(i)] = p
	}
	return decisionAnswer{
		Type:          "score",
		Score:         f64(1),
		Legend:        append([]string(nil), criteria...),
		Probabilities: probs,
	}, nil
}

// klines serves deterministic closed BTCUSDT candles for 1m, 5m and 1h.
func (s *stub) klines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeBinanceError(w, http.StatusMethodNotAllowed, -1000, "Method not allowed.")
		return
	}
	q := r.URL.Query()
	s.countKlines(q.Get("symbol"))
	if q.Get("symbol") != "BTCUSDT" {
		writeBinanceError(w, http.StatusBadRequest, -1121, "Invalid symbol.")
		return
	}
	step, ok := klineIntervals[q.Get("interval")]
	if !ok {
		writeBinanceError(w, http.StatusBadRequest, -1120, "Invalid interval.")
		return
	}
	limit := 500
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			writeBinanceError(w, http.StatusBadRequest, -1130,
				"Data sent for parameter 'limit' is not valid.")
			return
		}
		limit = n
	}
	startMs, err := optionalMs(q, "startTime")
	if err != nil {
		writeBinanceError(w, http.StatusBadRequest, -1130, err.Error())
		return
	}
	endMs, err := optionalMs(q, "endTime")
	if err != nil {
		writeBinanceError(w, http.StatusBadRequest, -1130, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.klinesRows(step, limit, startMs, endMs))
}

// klinesRows builds the closed candles selected by the query.
func (s *stub) klinesRows(step time.Duration, limit int, startMs, endMs *int64) [][]any {
	stepMs := step.Milliseconds()
	nowMs := s.now().UTC().UnixMilli()
	// The bar opening at lastClosed has already closed at now.
	lastClosed := ((nowMs - stepMs) / stepMs) * stepMs
	if endMs != nil {
		if end := (*endMs / stepMs) * stepMs; end < lastClosed {
			lastClosed = end
		}
	}
	var first int64
	if startMs != nil {
		first = ((*startMs + stepMs - 1) / stepMs) * stepMs
	} else {
		first = lastClosed - int64(limit-1)*stepMs
	}
	rows := make([][]any, 0, limit)
	for open := first; open <= lastClosed && len(rows) < limit; open += stepMs {
		rows = append(rows, syntheticKline(open, stepMs))
	}
	return rows
}

// syntheticKline returns one Binance-style kline row derived from the bar
// number, so the same bar always has the same OHLCV values.
func syntheticKline(openMs, stepMs int64) []any {
	n := openMs / stepMs
	open := 100000 + n%5000
	closePx := open + 10 + n%50
	high := max(open, closePx) + 5
	low := min(open, closePx) - 5
	volume := 1 + n%100
	return []any{
		openMs,
		strconv.FormatInt(open, 10),
		strconv.FormatInt(high, 10),
		strconv.FormatInt(low, 10),
		strconv.FormatInt(closePx, 10),
		strconv.FormatInt(volume, 10),
		openMs + stepMs - 1,
		"0", 0, "0", "0", "0",
	}
}

// optionalMs parses an optional non-negative millisecond query parameter.
func optionalMs(q url.Values, name string) (*int64, error) {
	raw := q.Get(name)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return nil, fmt.Errorf("%s must be a non-negative millisecond timestamp", name)
	}
	return &v, nil
}

// telegram accepts any bot token and answers sendMessage with ok=true. The
// token and the message text are deliberately never logged.
func (s *stub) telegram(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "description": "method not allowed"})
		return
	}
	s.countTelegram()
	token := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/bot"), "/sendMessage")
	if token == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "description": "unknown bot"})
		return
	}
	if status := s.roleStatus(true); status != http.StatusOK {
		writeJSON(w, status, map[string]any{"ok": false, "description": "smokestub telegram status"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]int{"message_id": 1}})
}

// stats serves the request counters by role. Only aggregate counters are
// returned: no token, message text or request body is ever stored or echoed.
func (s *stub) stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, s.snapshot())
}

// snapshot copies the counters under the lock.
func (s *stub) snapshot() stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	klines := make(map[string]int, len(s.klineRequests))
	for symbol, n := range s.klineRequests {
		klines[symbol] = n
	}
	return stats{
		Decision: s.decisionRequests,
		Telegram: s.telegramRequests,
		Klines:   klines,
	}
}

// countDecision records one decision role request.
func (s *stub) countDecision() {
	s.mu.Lock()
	s.decisionRequests++
	s.mu.Unlock()
}

// countTelegram records one Telegram role request.
func (s *stub) countTelegram() {
	s.mu.Lock()
	s.telegramRequests++
	s.mu.Unlock()
}

// countKlines records one klines role request for symbol.
func (s *stub) countKlines(symbol string) {
	s.mu.Lock()
	if s.klineRequests == nil {
		s.klineRequests = make(map[string]int)
	}
	s.klineRequests[symbol]++
	s.mu.Unlock()
}

// ctl switches the HTTP status of a role. Both parameters are optional.
func (s *stub) ctl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	q := r.URL.Query()
	decision, err := statusParam(q, "decision")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	telegram, err := statusParam(q, "telegram")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	if decision != nil {
		s.decisionStatus = *decision
	}
	if telegram != nil {
		s.telegramStatus = *telegram
	}
	body := map[string]int{"decision": s.decisionStatus, "telegram": s.telegramStatus}
	s.mu.Unlock()
	if s.logger != nil {
		s.logger.Printf("ctl decision=%d telegram=%d", body["decision"], body["telegram"])
	}
	writeJSON(w, http.StatusOK, body)
}

// statusParam parses an optional HTTP status in the 100..599 range.
func statusParam(q url.Values, name string) (*int, error) {
	raw := q.Get(name)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 100 || v > 599 {
		return nil, fmt.Errorf("%s must be an HTTP status in [100, 599]", name)
	}
	return &v, nil
}

// roleStatus returns the current HTTP status of a role.
func (s *stub) roleStatus(telegram bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if telegram {
		return s.telegramStatus
	}
	return s.decisionStatus
}

// writeJSON writes one JSON response with its status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeBinanceError writes a Binance-style error body.
func writeBinanceError(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, map[string]any{"code": code, "msg": msg})
}

// f64 returns a pointer to v for optional answer fields.
func f64(v float64) *float64 { return &v }

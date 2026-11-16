// Package journal implements the hash-chained audit event primitives.
//
// Chain rule: hash = hex(sha256(prevHash + "\n" + canonicalJSON)) where
// canonicalJSON is the event encoded without the hash field, with sorted
// keys and no whitespace. The first event uses ZeroHash as prevHash.
package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// ZeroHash is the prevHash of the first event in a chain.
const ZeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

// CanonicalizePayload validates raw as exactly one JSON value and returns
// its canonical form: object keys sorted, no whitespace, numbers preserved
// literally (json.Number), HTML escaping off. Empty or whitespace-only
// input means null. Duplicate object keys at any nesting depth are an
// error, as are trailing data after the top-level value.
func CanonicalizePayload(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("null"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := checkOneValue(dec); err != nil {
		return nil, fmt.Errorf("journal: invalid payload: %w", err)
	}
	var v any
	dec2 := json.NewDecoder(bytes.NewReader(raw))
	dec2.UseNumber()
	if err := dec2.Decode(&v); err != nil {
		return nil, fmt.Errorf("journal: invalid payload: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("journal: encode payload: %w", err)
	}
	return []byte(strings.TrimSuffix(buf.String(), "\n")), nil
}

// checkOneValue consumes exactly one JSON value from dec and rejects any
// trailing data after it.
func checkOneValue(dec *json.Decoder) error {
	if err := checkValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing data after top-level value")
		}
		return err
	}
	return nil
}

// checkValue consumes one JSON value, rejecting duplicate object keys at
// any nesting depth.
func checkValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			seen := make(map[string]struct{})
			for dec.More() {
				ktok, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := ktok.(string)
				if !ok {
					return fmt.Errorf("non-string object key")
				}
				if _, dup := seen[key]; dup {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := checkValue(dec); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil {
				return err
			}
			if d, ok := end.(json.Delim); !ok || d != '}' {
				return fmt.Errorf("unterminated object")
			}
			return nil
		case '[':
			for dec.More() {
				if err := checkValue(dec); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil {
				return err
			}
			if d, ok := end.(json.Delim); !ok || d != ']' {
				return fmt.Errorf("unterminated array")
			}
			return nil
		default:
			return fmt.Errorf("unexpected delimiter %q", string(t))
		}
	case string, bool, nil, json.Number:
		return nil
	default:
		return fmt.Errorf("unexpected JSON value of type %T", tok)
	}
}

// CanonicalJSON encodes ev without the hash field: keys sorted, no spaces.
// The timestamp is formatted as UTC RFC3339Nano; the payload bytes are the
// canonical payload embedded verbatim via json.RawMessage (never
// decode/re-encoded, so duplicate keys cannot silently collapse). Optional
// fields (runId, decisionId, policyHash, snapshotHash) are included only
// when non-empty.
func CanonicalJSON(ev domain.Event) ([]byte, error) {
	payload, err := CanonicalizePayload(ev.Payload)
	if err != nil {
		return nil, err
	}
	m := map[string]any{
		"codeVersion":   ev.CodeVersion,
		"eventId":       ev.EventID,
		"kind":          ev.Kind,
		"payload":       json.RawMessage(payload),
		"prevHash":      ev.PrevHash,
		"schemaVersion": ev.SchemaVersion,
		"seq":           ev.Seq,
		"ts":            ev.TS.UTC().Format(time.RFC3339Nano),
	}
	if ev.RunID != "" {
		m["runId"] = ev.RunID
	}
	if ev.DecisionID != "" {
		m["decisionId"] = ev.DecisionID
	}
	if ev.PolicyHash != "" {
		m["policyHash"] = ev.PolicyHash
	}
	if ev.SnapshotHash != "" {
		m["snapshotHash"] = ev.SnapshotHash
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("journal: encode canonical: %w", err)
	}
	return []byte(strings.TrimSuffix(buf.String(), "\n")), nil
}

// Hash returns hex(sha256(prevHash + "\n" + canonical)).
func Hash(prevHash string, canonical []byte) string {
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write([]byte("\n"))
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil))
}

// Seal sets PrevHash and computes Hash for ev.
func Seal(ev domain.Event, prevHash string) (domain.Event, error) {
	ev.PrevHash = prevHash
	ev.Hash = ""
	canonical, err := CanonicalJSON(ev)
	if err != nil {
		return domain.Event{}, err
	}
	ev.Hash = Hash(prevHash, canonical)
	return ev, nil
}

// errChainBroken stops the walk once the first break is recorded.
var errChainBroken = errors.New("journal: chain broken")

// verifyStep checks one event against the running chain state and advances
// it. It reports the first break through broken; ok is false when the
// chain breaks at ev.
func verifyStep(state *verifyState, ev domain.Event) (broken *domain.VerifyResult, ok bool) {
	if want := state.count + 1; ev.Seq != want {
		return &domain.VerifyResult{
			OK:        false,
			Count:     state.count,
			BrokenSeq: ev.Seq,
			Reason:    fmt.Sprintf("seq gap: want %d got %d", want, ev.Seq),
		}, false
	}
	wantPrev := ZeroHash
	if state.count > 0 {
		wantPrev = state.prevHash
	}
	if ev.PrevHash != wantPrev {
		return &domain.VerifyResult{
			OK:        false,
			Count:     state.count,
			BrokenSeq: ev.Seq,
			Reason:    fmt.Sprintf("prevHash mismatch at seq %d", ev.Seq),
		}, false
	}
	canon, err := CanonicalizePayload(ev.Payload)
	if err != nil {
		return &domain.VerifyResult{
			OK:        false,
			Count:     state.count,
			BrokenSeq: ev.Seq,
			Reason:    fmt.Sprintf("payload canonicalization failed at seq %d: %v", ev.Seq, err),
		}, false
	}
	if !bytes.Equal(canon, ev.Payload) {
		return &domain.VerifyResult{
			OK:        false,
			Count:     state.count,
			BrokenSeq: ev.Seq,
			Reason:    fmt.Sprintf("payload not canonical at seq %d", ev.Seq),
		}, false
	}
	canonical, err := CanonicalJSON(ev)
	if err != nil {
		return &domain.VerifyResult{
			OK:        false,
			Count:     state.count,
			BrokenSeq: ev.Seq,
			Reason:    fmt.Sprintf("canonicalize seq %d: %v", ev.Seq, err),
		}, false
	}
	if got := Hash(ev.PrevHash, canonical); got != ev.Hash {
		return &domain.VerifyResult{
			OK:        false,
			Count:     state.count,
			BrokenSeq: ev.Seq,
			Reason:    fmt.Sprintf("hash mismatch at seq %d", ev.Seq),
		}, false
	}
	state.count++
	state.prevHash = ev.Hash
	return nil, true
}

// verifyState is the running chain state shared by Verify and VerifyStream.
type verifyState struct {
	count    int64
	prevHash string
}

// VerifyStream checks the chain delivered by walk without holding all
// events in memory. walk must call fn for each event in ascending seq
// order; an error returned by fn stops the walk. An empty stream is valid
// with Count 0.
func VerifyStream(ctx context.Context, walk func(ctx context.Context, fn func(domain.Event) error) error) (domain.VerifyResult, error) {
	state := &verifyState{}
	var broken *domain.VerifyResult
	err := walk(ctx, func(ev domain.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if b, ok := verifyStep(state, ev); !ok {
			broken = b
			return errChainBroken
		}
		return nil
	})
	if broken != nil {
		return *broken, nil
	}
	if err != nil {
		return domain.VerifyResult{}, err
	}
	return domain.VerifyResult{OK: true, Count: state.count}, nil
}

// Verify checks the whole chain and returns the first break, if any.
// An empty chain is valid with Count 0.
func Verify(events []domain.Event) domain.VerifyResult {
	res, err := VerifyStream(context.Background(), func(_ context.Context, fn func(domain.Event) error) error {
		for _, ev := range events {
			if err := fn(ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.VerifyResult{Reason: err.Error()}
	}
	return res
}

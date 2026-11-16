// Package journal implements the hash-chained audit event primitives.
//
// Chain rule: hash = hex(sha256(prevHash + "\n" + canonicalJSON)) where
// canonicalJSON is the event encoded without the hash field, with sorted
// keys and no whitespace. The first event uses ZeroHash as prevHash.
package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
)

// ZeroHash is the prevHash of the first event in a chain.
const ZeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

// CanonicalJSON encodes ev without the hash field: keys sorted, no spaces.
// The timestamp is formatted as UTC RFC3339Nano; payload is decoded with
// json.Number so numbers keep their precision. Optional fields
// (runId, decisionId, policyHash, snapshotHash) are included only when
// non-empty.
func CanonicalJSON(ev domain.Event) ([]byte, error) {
	var payload any
	if len(ev.Payload) > 0 {
		dec := json.NewDecoder(bytes.NewReader(ev.Payload))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			return nil, fmt.Errorf("journal: decode payload: %w", err)
		}
	}
	m := map[string]any{
		"codeVersion":   ev.CodeVersion,
		"eventId":       ev.EventID,
		"kind":          ev.Kind,
		"payload":       payload,
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

// Verify checks the whole chain and returns the first break, if any.
// An empty chain is valid with Count 0.
func Verify(events []domain.Event) domain.VerifyResult {
	if len(events) == 0 {
		return domain.VerifyResult{OK: true, Count: 0}
	}
	for i, ev := range events {
		want := int64(i + 1)
		if ev.Seq != want {
			return domain.VerifyResult{
				OK:        false,
				Count:     int64(i),
				BrokenSeq: ev.Seq,
				Reason:    fmt.Sprintf("seq gap: want %d got %d", want, ev.Seq),
			}
		}
		wantPrev := ZeroHash
		if i > 0 {
			wantPrev = events[i-1].Hash
		}
		if ev.PrevHash != wantPrev {
			return domain.VerifyResult{
				OK:        false,
				Count:     int64(i),
				BrokenSeq: ev.Seq,
				Reason:    fmt.Sprintf("prevHash mismatch at seq %d", ev.Seq),
			}
		}
		canonical, err := CanonicalJSON(ev)
		if err != nil {
			return domain.VerifyResult{
				OK:        false,
				Count:     int64(i),
				BrokenSeq: ev.Seq,
				Reason:    fmt.Sprintf("canonicalize seq %d: %v", ev.Seq, err),
			}
		}
		if got := Hash(ev.PrevHash, canonical); got != ev.Hash {
			return domain.VerifyResult{
				OK:        false,
				Count:     int64(i),
				BrokenSeq: ev.Seq,
				Reason:    fmt.Sprintf("hash mismatch at seq %d", ev.Seq),
			}
		}
	}
	return domain.VerifyResult{OK: true, Count: int64(len(events))}
}

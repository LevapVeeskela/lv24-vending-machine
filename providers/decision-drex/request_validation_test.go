package decisiondrex_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LevapVeeskela/lv24-vending-machine/core/domain"
	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
	decisiondrex "github.com/LevapVeeskela/lv24-vending-machine/providers/decision-drex"
)

type stubSecrets map[string]string

func (m stubSecrets) Get(_ context.Context, ref string) (string, error) {
	v, ok := m[ref]
	if !ok || v == "" {
		return "", context.DeadlineExceeded
	}
	return v, nil
}

func choiceQuestion(id string, n int) domain.Question {
	opts := make([]domain.Option, n)
	for i := range opts {
		opts[i] = domain.Option{Key: "k" + string(rune('0'+i)), Description: "d"}
	}
	return domain.Question{ID: id, Type: domain.QuestionChoice, Instructions: "pick", Options: opts}
}

// TestDecideRejectsBadRequestsLocally proves local validation fires with
// invalid_request before any HTTP round trip.
func TestDecideRejectsBadRequestsLocally(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	deps := ports.Deps{Secrets: stubSecrets{"ref": "s3cr3t"}}
	mk := func() *decisiondrex.Provider {
		t.Helper()
		p, err := decisiondrex.New(decisiondrex.Config{
			Profile:   decisiondrex.ProfileDrex,
			BaseURL:   srv.URL,
			SecretRef: "ref",
		}, deps)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	}

	noul := domain.Question{ID: "q", Type: domain.QuestionNoul, Instructions: "?"}
	cases := map[string]domain.DecisionRequest{
		"empty model": {
			Questions: []domain.Question{noul},
		},
		"no questions": {
			Model: "m",
		},
		"duplicate ids": {
			Model:     "m",
			Questions: []domain.Question{noul, noul},
		},
		"choice single option": {
			Model:     "m",
			Questions: []domain.Question{choiceQuestion("c", 1)},
		},
		"unknown type": {
			Model:     "m",
			Questions: []domain.Question{{ID: "x", Type: "bogus"}},
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			before := hits.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := mk().Decide(ctx, req)
			if domain.CodeOf(err) != domain.CodeInvalidRequest {
				t.Fatalf("expected invalid_request, got %v", err)
			}
			if hits.Load() != before {
				t.Fatalf("request reached HTTP for %s", name)
			}
		})
	}
}

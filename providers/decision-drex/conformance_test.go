package decisiondrex_test

import (
	"testing"

	"github.com/LevapVeeskela/lv24-vending-machine/core/ports"
	"github.com/LevapVeeskela/lv24-vending-machine/core/porttest"
	decisiondrex "github.com/LevapVeeskela/lv24-vending-machine/providers/decision-drex"
)

func TestDecisionConformance(t *testing.T) {
	porttest.DecisionConformance(t, func(t *testing.T, opts porttest.DecisionOptions, deps ports.Deps) ports.Decision {
		t.Helper()
		cfg := decisiondrex.Config{
			Profile:         decisiondrex.ProfileDrex,
			SecretRef:       porttest.DecisionSecretRef,
			PricePerBillion: opts.PricePerBillion,
			BaseURL:         opts.BaseURL,
		}
		p, err := decisiondrex.New(cfg, deps)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	})
}

func TestDecisionBaseURLPolicy(t *testing.T) {
	porttest.DecisionBaseURLConformance(t, func(opts porttest.DecisionOptions, deps ports.Deps) (ports.Decision, error) {
		return decisiondrex.New(decisiondrex.Config{
			Profile:         decisiondrex.ProfileDrex,
			SecretRef:       porttest.DecisionSecretRef,
			PricePerBillion: opts.PricePerBillion,
			BaseURL:         opts.BaseURL,
		}, deps)
	})
}

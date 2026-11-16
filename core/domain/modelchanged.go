package domain

import "time"

// ModelChanged records a decision vendor model substitution.
type ModelChanged struct {
	RequestedModel string    `json:"requestedModel"`
	PreviousModel  string    `json:"previousModel"`
	ActualModel    string    `json:"actualModel"`
	DetectedAt     time.Time `json:"detectedAt"`
}

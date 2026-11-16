package domain

import "time"

// Outcome is a labeled trading outcome for one signal.
type Outcome struct {
	SignalID    string     `json:"signalId"`
	DecisionID  string     `json:"decisionId,omitempty"`
	Instrument  Instrument `json:"instrument"`
	Side        Side       `json:"side"`
	Timeframe   string     `json:"timeframe"`
	EntryTS     time.Time  `json:"entryTs"`
	EntryPrice  Decimal    `json:"entryPrice"`
	ExitTS      time.Time  `json:"exitTs"`
	ExitPrice   Decimal    `json:"exitPrice"`
	HorizonBars int        `json:"horizonBars"`
	FeeBps      int        `json:"feeBps"`
	GrossReturn Decimal    `json:"grossReturn"`
	NetReturn   Decimal    `json:"netReturn"`
	MFE         Decimal    `json:"mfe"`
	MAE         Decimal    `json:"mae"`
	CandlesHash string     `json:"candlesHash"`
	LabeledAt   time.Time  `json:"labeledAt"`
}

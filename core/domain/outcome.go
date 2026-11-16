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
	// FeeBps is the one-way fee in basis points; NetReturn subtracts the
	// round-trip fee, 2 * feeBps / 10000.
	FeeBps      int     `json:"feeBps"`
	GrossReturn Decimal `json:"grossReturn"`
	NetReturn   Decimal `json:"netReturn"`
	MFE         Decimal `json:"mfe"`
	MAE         Decimal `json:"mae"`
	// CandlesHash is the lowercase hex SHA-256 of the canonical JSON of
	// exactly the used bars (decimals normalized to 12 decimal places).
	CandlesHash string    `json:"candlesHash"`
	LabeledAt   time.Time `json:"labeledAt"`
	// ReceivedAt is the journal time when gate.request was accepted.
	ReceivedAt time.Time `json:"receivedAt,omitempty"`
	// Backfill marks signals that arrived later than one timeframe interval.
	Backfill bool `json:"backfill,omitempty"`
	// PriceSource is the candle provider id used for labeling.
	PriceSource string `json:"priceSource,omitempty"`
}

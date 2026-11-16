package domain

import "time"

// Candle is a single OHLCV market data point.
type Candle struct {
	Symbol    string    `json:"symbol"`
	Interval  string    `json:"interval"`
	OpenTime  time.Time `json:"openTime"`
	CloseTime time.Time `json:"closeTime"`
	Open      Decimal   `json:"open"`
	High      Decimal   `json:"high"`
	Low       Decimal   `json:"low"`
	Close     Decimal   `json:"close"`
	Volume    Decimal   `json:"volume"`
}

// CandleRequest selects a candle range from a market data provider.
type CandleRequest struct {
	Symbol   string    `json:"symbol"`
	Interval string    `json:"interval"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Limit    int       `json:"limit"`
}

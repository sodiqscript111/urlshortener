package api

import (
	"time"

	"github.com/sony/gobreaker"
)

var DBCircuitBreaker *gobreaker.CircuitBreaker

func InitBreakers() {
	st := gobreaker.Settings{
		Name:        "DB",
		MaxRequests: 5,
		Interval:    10 * time.Second,
		Timeout:     5 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= 3 && failureRatio >= 0.6
		},
	}
	DBCircuitBreaker = gobreaker.NewCircuitBreaker(st)
}

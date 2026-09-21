package storage

import (
	"math"
	"sort"
	"time"
)

const (
	quotaForecastLookback   = 14 * 24 * time.Hour
	quotaForecastMinSpacing = time.Minute
	quotaForecastResetDrop  = 5.0
)

type quotaObservation struct {
	At         time.Time
	Used       float64
	ResetAtMS  int64
	WindowKind string
}

type accountQuotaObservations struct {
	Primary   []quotaObservation
	Secondary []quotaObservation
}

func (o *accountQuotaObservations) addPrimary(observation quotaObservation) {
	o.Primary = append(o.Primary, observation)
}

func (o *accountQuotaObservations) addSecondary(observation quotaObservation) {
	o.Secondary = append(o.Secondary, observation)
}

func buildQuotaForecast(window *AccountQuotaWindowDetail, observations []quotaObservation, now time.Time) *AccountQuotaForecast {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if window == nil || (window.WindowKind != "five_hour" && window.WindowKind != "weekly") {
		return nil
	}

	if window.UsedPercent == nil || window.ResetAtMS <= 0 || window.ResetAtMS <= now.UnixMilli() {
		return nil
	}

	current := make([]quotaObservation, 0, len(observations))
	for _, observation := range observations {
		if observation.WindowKind != window.WindowKind || observation.ResetAtMS <= 0 ||
			!sameQuotaReset(observation.ResetAtMS, window.ResetAtMS) ||
			observation.At.IsZero() || observation.At.After(now.Add(5*time.Minute)) || observation.At.After(now) ||
			math.IsNaN(observation.Used) || math.IsInf(observation.Used, 0) || observation.Used < 0 || observation.Used > 100 {
			continue
		}
		current = append(current, observation)
	}
	if len(current) == 0 {
		return nil
	}

	sort.SliceStable(current, func(i, j int) bool {
		return current[i].At.Before(current[j].At)
	})

	// A sudden decrease in used percentage marks a reset or an upstream
	// re-baseline. Do not estimate across that boundary, even when the reset
	// timestamp was omitted or not updated by the upstream.
	start := 0
	for i := 1; i < len(current); i++ {
		if current[i].Used+quotaForecastResetDrop < current[i-1].Used {
			start = i
		}
	}
	current = current[start:]
	if len(current) < 2 {
		return nil
	}
	forecast := &AccountQuotaForecast{SampleCount: len(current)}

	if !isFinitePercent(*window.UsedPercent) {
		return nil
	}
	if *window.UsedPercent >= 100 {
		forecast.Status = "exhausted"
		forecast.EstimatedExhaustionAtMS = now.UnixMilli()
		return forecast
	}
	maxRate := 0.0
	for i := 1; i < len(current); i++ {
		elapsed := current[i].At.Sub(current[i-1].At)
		if elapsed < quotaForecastMinSpacing {
			continue
		}
		delta := current[i].Used - current[i-1].Used
		if delta <= 0 {
			continue
		}
		rate := delta / elapsed.Hours()
		if rate > maxRate && !math.IsNaN(rate) && !math.IsInf(rate, 0) {
			maxRate = rate
		}
	}
	if maxRate <= 0 {
		return nil
	}

	remaining := 100.0 - *window.UsedPercent
	if remaining <= 0 {
		forecast.Status = "exhausted"
		forecast.EstimatedExhaustionAtMS = now.UnixMilli()
		return forecast
	}

	seconds := remaining / maxRate * float64(time.Hour/time.Second)
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return forecast
	}
	forecast.BurnRatePercentPerHour = maxRate
	projectedAt := now.Add(time.Duration(seconds * float64(time.Second)))
	if projectedAt.UnixMilli() >= window.ResetAtMS {
		forecast.Status = "after_reset"
		return forecast
	}
	forecast.Status = "available"
	forecast.EstimatedExhaustionAfterSec = int64(math.Ceil(seconds))
	forecast.EstimatedExhaustionAtMS = projectedAt.UnixMilli()
	return forecast
}

func isFinitePercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func sameQuotaReset(left, right int64) bool {
	const toleranceMS = 5 * 60 * 1000
	return left > 0 && right > 0 && math.Abs(float64(left-right)) <= toleranceMS
}

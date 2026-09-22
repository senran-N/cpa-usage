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
	At          time.Time
	Used        float64
	ResetAtMS   int64
	WindowKind  string
	TotalTokens int64
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
	span := current[len(current)-1].At.Sub(current[0].At)
	if span < quotaForecastMinSpacing {
		// Not enough history to extrapolate anything.
		return nil
	}
	maxRate := (current[len(current)-1].Used - current[0].Used) / span.Hours()
	// Upstream reports used percent as a coarse integer step while sampling far
	// more often than once a minute, so neighbouring samples almost never both
	// clear the minimum spacing and show an increase. Keep the sustained span
	// rate above as the baseline and raise it to the steepest burst actually
	// observed.
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
	if maxRate <= 0 || math.IsNaN(maxRate) || math.IsInf(maxRate, 0) {
		// Consumption is flat over the window: there is no trend to extrapolate.
		// Report that explicitly instead of showing nothing at all.
		forecast.Status = "stable"
		return forecast
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

// applyWindowEstimates back-calculates an absolute allowance for one quota
// window. Upstream only reports a usage percentage, so the total allowance is
// inferred from the consumption this proxy recorded inside that window and the
// percentage it reported. The result is an estimate, never an accounting
// figure: it only covers requests that passed through here, and upstream may
// weight model tiers differently.
func applyWindowEstimates(window *AccountQuotaWindowDetail, observations []quotaObservation, now time.Time) {
	if window == nil || len(observations) == 0 {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var tokens int64
	var requests int64
	for _, observation := range observations {
		if observation.WindowKind != window.WindowKind || observation.ResetAtMS <= 0 ||
			!sameQuotaReset(observation.ResetAtMS, window.ResetAtMS) ||
			observation.At.IsZero() || observation.At.After(now) {
			continue
		}
		if observation.TotalTokens > 0 {
			tokens += observation.TotalTokens
		}
		requests++
	}
	window.ConsumedTokens = tokens
	window.ConsumedRequests = requests

	used := window.UsedPercent
	// A barely-moved percentage makes the back-calculation explode (one rounding
	// step at 1% implies an allowance orders of magnitude above what was seen),
	// so only publish an absolute estimate once the window has meaningfully moved
	// and this proxy recorded real consumption in it.
	if used == nil || !isFinitePercent(*used) || *used < 1.0 || *used > 100.0 || tokens <= 0 {
		return
	}
	total := int64(math.Round(float64(tokens) / (*used / 100.0)))
	remainingTokens := total - tokens
	if remainingTokens < 0 {
		remainingTokens = 0
	}
	remainingRequests := int64(math.Round(float64(requests) * (100.0 - *used) / *used))
	if remainingRequests < 0 {
		remainingRequests = 0
	}
	window.EstimatedTotalTokens = &total
	window.EstimatedRemainingTokens = &remainingTokens
	window.EstimatedRemainingRequests = &remainingRequests
}

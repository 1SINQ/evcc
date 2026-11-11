package core

import (
	"math"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/tariff"
	"github.com/stretchr/testify/assert"
)

func TestDemandPlanEnergy(t *testing.T) {
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	rate := func(i int, w float64) api.Rate {
		s := start.Add(time.Duration(i) * tariff.SlotDuration)
		return api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: w}
	}
	// 4 slots at 1000 W = 0.25 kWh each, then a gap
	rr := api.Rates{rate(0, 1000), rate(1, 1000), rate(2, 2000), rate(3, 0), rate(5, 4000)}
	res, covered := demandPlanEnergy(rr, start, 8)
	assert.Equal(t, 4, covered, "coverage ends at the first gap")
	assert.InDeltaSlice(t, []float64{0.25, 0.25, 0.5, 0}, res[:4], 1e-9)

	_, covered = demandPlanEnergy(rr, start.Add(3*time.Hour), 8)
	assert.Zero(t, covered, "plan ended before now")

	_, covered = demandPlanEnergy(api.Rates{rate(0, math.NaN())}, start, 8)
	assert.Zero(t, covered, "invalid values end coverage")

	_, covered = demandPlanEnergy(api.Rates{rate(0, -1)}, start, 8)
	assert.Zero(t, covered, "negative values end coverage")

	_, covered = demandPlanEnergy(rr, start, 2)
	assert.Equal(t, 2, covered, "coverage is limited to the horizon")
}

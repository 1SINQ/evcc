package core

import (
	"slices"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/settings"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/jinzhu/now"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestApplyTemperatureCorrection(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	now := time.Now().Truncate(15 * time.Minute)

	mockTariff := api.NewMockTariff(ctrl)

	rates := []api.Rate{}

	// Past 7 days - create slots for ALL hours to ensure historical data exists
	for day := -7; day < 0; day++ {
		for hour := range 24 {
			for slot := range 4 {
				baseTime := now.AddDate(0, 0, day).Truncate(24 * time.Hour)
				rates = append(rates, api.Rate{
					Start: baseTime.Add(time.Duration(hour)*time.Hour + time.Duration(slot)*15*time.Minute),
					End:   baseTime.Add(time.Duration(hour)*time.Hour + time.Duration(slot+1)*15*time.Minute),
					Value: 10.0,
				})
			}
		}
	}

	// Future forecast: 12 slots (3 hours)
	// First hour: 5°C, second hour: 15°C, third hour: 20°C
	for i := range 12 {
		temp := 5.0
		switch {
		case i >= 8:
			temp = 20.0
		case i >= 4:
			temp = 15.0
		}
		rates = append(rates, api.Rate{
			Start: now.Add(time.Duration(i) * 15 * time.Minute),
			End:   now.Add(time.Duration(i+1) * 15 * time.Minute),
			Value: temp,
		})
	}

	mockTariff.EXPECT().Rates().Return(rates, nil).AnyTimes()

	site := &Site{
		log:     util.NewLogger("test"),
		tariffs: &tariff.Tariffs{Temperature: mockTariff},
	}

	profile := slices.Repeat([]float64{2.0}, 12)

	// 1. Linear scaling (binned == nil)
	result := site.applyTemperatureCorrection("test", profile, nil)

	require.Len(t, result, 12)

	// Verify correction is applied: first hour should increase, second hour should decrease
	assert.Greater(t, result[0], 2.0, "first hour should increase (colder forecast)")
	assert.Less(t, result[4], 2.0, "second hour should decrease (warmer forecast)")
	assert.Greater(t, result[0], result[4], "first hour should be higher than second hour")
	// warm forecast scales down naturally and clamps to minCorrection (10%)
	assert.InDelta(t, 0.2, result[8], 1e-6, "third hour scales to minCorrection floor")

	// 2. Temperature-binned lookup: same bins for every slot of the day, so the
	// result depends on the forecast temperature only
	bins := map[int]float64{
		5:  3.5, // 5°C forecast
		10: 2.2,
		12: 1.0,
		20: 0.8, // 20°C forecast, recorded DHW cycle
	}
	binned := make(map[int]map[int]float64)
	for slot := range 96 {
		binned[slot] = bins
	}
	resultBinned := site.applyTemperatureCorrection("test", profile, binned)
	assert.InDelta(t, 3.5, resultBinned[0], 1e-6, "binned exact match should be used directly")
	assert.Less(t, resultBinned[4], 2.0, "15°C without bin falls back to scaling")
	assert.InDelta(t, 0.8, resultBinned[8], 1e-6, "binned exact match for warm temp (>=18°C) should be used directly")
}

func TestLookupBin(t *testing.T) {
	bins := map[int]float64{
		5:  3.0,
		7:  5.0,
		10: 2.0,
		12: 4.0,
	}

	// Exact match
	val, ok := lookupBin(bins, 5.2)
	assert.True(t, ok)
	assert.InDelta(t, 3.0, val, 1e-6)

	// Adjacent ±1 interpolation (e.g. 11°C interpolates between 10°C and 12°C)
	val, ok = lookupBin(bins, 11.0)
	assert.True(t, ok)
	assert.InDelta(t, 3.0, val, 1e-6) // (2.0 + 4.0) / 2

	// Adjacent single match (e.g. 6°C matches 5°C and 7°C)
	val, ok = lookupBin(bins, 6.0)
	assert.True(t, ok)
	assert.InDelta(t, 4.0, val, 1e-6) // (3.0 + 5.0) / 2

	// Adjacent single +1 match (e.g. 4°C matches bin 5)
	val, ok = lookupBin(bins, 4.0)
	assert.True(t, ok)
	assert.InDelta(t, 3.0, val, 1e-6)

	// No match (e.g. 25°C)
	_, ok = lookupBin(bins, 25.0)
	assert.False(t, ok)

	// Nil bins
	_, ok = lookupBin(nil, 5.0)
	assert.False(t, ok)
}

func TestTileAndTrim(t *testing.T) {
	profile := make([]float64, 96)
	for i := range profile {
		profile[i] = float64(i)
	}

	res := tileAndTrim(profile, 200)
	require.Len(t, res, 200)

	firstSlot := int(time.Now().Truncate(tariff.SlotDuration).Sub(now.BeginningOfDay()) / tariff.SlotDuration)
	assert.Equal(t, float64(firstSlot), res[0], "starts at the current slot")
	assert.Equal(t, res[0], res[96], "wraps after a full day")
}

// slotRates returns n rates of the given power starting at start
func slotRates(start time.Time, n int, w float64) api.Rates {
	var rr api.Rates
	for i := range n {
		s := start.Add(time.Duration(i) * tariff.SlotDuration)
		rr = append(rr, api.Rate{Start: s, End: s.Add(tariff.SlotDuration), Value: w})
	}
	return rr
}

// newHistoryCollector returns a collector with the given energy (kWh) in every slot of the past days
func newHistoryCollector(t *testing.T, group, name string, energy float64) *metrics.Collector {
	t.Helper()

	col, err := metrics.NewCollector(group, name, name)
	require.NoError(t, err)

	var id int
	require.NoError(t, db.Instance.Raw(`SELECT id FROM entities WHERE name = ?`, name).Scan(&id).Error)
	for day := -3; day < 0; day++ {
		base := now.BeginningOfDay().AddDate(0, 0, day)
		for slot := range 96 {
			ts := base.Add(time.Duration(slot) * tariff.SlotDuration)
			require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy, return_energy) VALUES (?, ?, ?, 0)`, id, ts.Unix(), energy).Error)
		}
	}

	return col
}

// newHeatingDemandSite returns a site with a single connected heating loadpoint whose
// charge history is 0.1 kWh in every slot of the past days. The loadpoint clock is
// fixed at start, the beginning of the current slot.
func newHeatingDemandSite(t *testing.T, charger api.Charger) (*Site, *Loadpoint, time.Time) {
	t.Helper()

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	col := newHistoryCollector(t, metrics.Loadpoint, "heater", 0.1)

	start := time.Now().Truncate(tariff.SlotDuration)
	clk := clock.NewMock()
	clk.Set(start)

	lp := NewLoadpoint(util.NewLogger("foo"), settings.NewDatabaseSettingsAdapter("foo"))
	lp.clock = clk
	lp.charger = charger
	lp.chargeEnergy = col
	lp.status = api.StatusC
	lp.mode = api.ModeNow

	site := NewSite()
	site.log = util.NewLogger("foo")
	site.loadpoints = []*Loadpoint{lp}

	return site, lp, start
}

func TestAddHeatingDemandSchedule(t *testing.T) {
	const minLen = 8

	var rr api.Rates
	var calls int
	plan := func() (api.Rates, error) {
		calls++
		return rr, nil
	}

	site, lp, start := newHeatingDemandSite(t, newPlanCharger(plan, api.Heating))
	rr = slotRates(start, 4, 1000)

	gt := make([]float64, minLen)
	res := site.addHeatingDemand(gt, minLen)

	require.Len(t, res, 1)
	assert.Equal(t, "schedule", res[0].source)
	assert.InDeltaSlice(t, []float64{250, 250, 250, 250, 100, 100, 100, 100}, gt, 1e-6, "schedule first, history behind")
	assert.InDeltaSlice(t, gt, res[0].values, 1e-6)
	assert.Equal(t, 1, calls)

	// slots are aligned by the loadpoint clock: one slot later the schedule covers 3 slots
	lp.clock.(*clock.Mock).Add(tariff.SlotDuration)

	gt = make([]float64, minLen)
	site.addHeatingDemand(gt, minLen)
	assert.InDeltaSlice(t, []float64{250, 250, 250, 100, 100, 100, 100, 100}, gt, 1e-6, "past slots do not count")
	lp.clock.(*clock.Mock).Set(start)

	// without history the schedule still counts, uncovered slots are empty
	require.NoError(t, db.Instance.Exec(`DELETE FROM meters`).Error)
	rr = slotRates(start, 4, 2000)

	gt = make([]float64, minLen)
	res = site.addHeatingDemand(gt, minLen)

	require.Len(t, res, 1)
	assert.Equal(t, "schedule", res[0].source)
	assert.InDeltaSlice(t, []float64{500, 500, 500, 500, 0, 0, 0, 0}, gt, 1e-6, "schedule without history")
}

func TestAddHeatingDemandHistory(t *testing.T) {
	const minLen = 8

	var err error
	var calls int
	plan := func() (api.Rates, error) {
		calls++
		return nil, err
	}

	site, lp, _ := newHeatingDemandSite(t, newPlanCharger(plan, api.Heating))

	// schedule unavailable: history fallback
	err = api.ErrNotAvailable

	gt := make([]float64, minLen)
	res := site.addHeatingDemand(gt, minLen)

	require.Len(t, res, 1)
	assert.Equal(t, "daily", res[0].source, "history predictor without schedule")
	assert.InDeltaSlice(t, slices.Repeat([]float64{100}, minLen), gt, 1e-6)
	assert.Equal(t, 1, calls)

	// history predictor selected: schedule is not requested
	lp.SetDemandPredictor("daily")

	gt = make([]float64, minLen)
	res = site.addHeatingDemand(gt, minLen)

	require.Len(t, res, 1)
	assert.Equal(t, "daily", res[0].source)
	assert.InDeltaSlice(t, slices.Repeat([]float64{100}, minLen), gt, 1e-6)
	assert.Equal(t, 1, calls, "schedule is not requested for history predictors")
}

func TestOptimizerRequestHeatingSource(t *testing.T) {
	ctrl := gomock.NewController(t)

	var rr api.Rates
	plan := func() (api.Rates, error) { return rr, nil }

	site, _, start := newHeatingDemandSite(t, newPlanCharger(plan, api.Heating))
	site.collectors[metrics.Home] = newHistoryCollector(t, metrics.Home, metrics.Home, 0.2)

	grid := api.NewMockTariff(ctrl)
	grid.EXPECT().Rates().Return(slotRates(start, 96, 0.3), nil).AnyTimes()
	grid.EXPECT().Type().Return(api.TariffTypePriceForecast).AnyTimes()
	site.tariffs = &tariff.Tariffs{Grid: grid}

	source := func() string {
		_, details, err := site.optimizerRequest(nil)
		require.NoError(t, err)
		require.Len(t, details.DemandDetails, 2)
		assert.Equal(t, demandTypeHeating, details.DemandDetails[1].Type)
		return details.DemandDetails[1].Source
	}

	assert.Equal(t, "daily", source(), "history without schedule")

	rr = slotRates(start, 4, 1000)
	assert.Equal(t, "schedule", source())
}

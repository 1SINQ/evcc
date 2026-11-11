package charger

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/plugin"
	"github.com/evcc-io/evcc/plugin/pipeline"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/templates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

func TestDemandPlanGetter(t *testing.T) {
	cfg := &plugin.Config{
		Source: "const",
		Other: map[string]any{
			"value": `[{"start":"2026-11-10T13:15:00Z","end":"2026-11-10T13:30:00Z","value":2500},` +
				`{"start":"2026-11-10T13:00:00Z","end":"2026-11-10T13:15:00Z","value":2000}]`,
		},
	}

	planG, err := demandPlanGetter(t.Context(), cfg)
	require.NoError(t, err)
	require.NotNil(t, planG)

	rates, err := planG()
	require.NoError(t, err)

	// sorted by start
	assert.Equal(t, api.Rates{
		{Start: time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 10, 13, 15, 0, 0, time.UTC), Value: 2000},
		{Start: time.Date(2026, 11, 10, 13, 15, 0, 0, time.UTC), End: time.Date(2026, 11, 10, 13, 30, 0, 0, time.UTC), Value: 2500},
	}, rates)
}

func TestDemandPlanGetterInvalidJson(t *testing.T) {
	cfg := &plugin.Config{
		Source: "const",
		Other:  map[string]any{"value": `[{"start":`},
	}

	planG, err := demandPlanGetter(t.Context(), cfg)
	require.NoError(t, err)

	_, err = planG()
	assert.Error(t, err)
}

// a monitored source with timeout (mqtt) blocks its first read until the timeout,
// the plan getter must not pass that wait on to the optimizer
func TestDemandPlanGetterNoBlockingWithoutMessage(t *testing.T) {
	mon := util.NewMonitor[string](time.Hour)
	planG := newDemandPlanGetter(mon.Get)

	// let the background warm-up take the one-time wait. Retrying reads would not do:
	// the first blocked read takes that wait itself and makes later reads pass anyway
	time.Sleep(200 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := planG()
		done <- err
	}()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, api.ErrOutdated)
	case <-time.After(5 * time.Second):
		t.Fatal("DemandPlan blocked without message")
	}

	// a plan arriving later is returned
	mon.Set(`[{"start":"2026-11-10T13:00:00Z","end":"2026-11-10T13:15:00Z","value":2000}]`)
	rates, err := planG()
	require.NoError(t, err)
	require.Len(t, rates, 1)
}

func TestDemandPlanGetterNil(t *testing.T) {
	planG, err := demandPlanGetter(t.Context(), nil)
	require.NoError(t, err)
	assert.Nil(t, planG)
}

// onekeyDemandPlan converts a onekey/wp/plan payload using the jq query of the onekey template
func onekeyDemandPlan(t *testing.T, payload []byte) (api.Rates, map[string]any) {
	t.Helper()

	tmpl, err := templates.ByName(templates.Charger, "onekey")
	require.NoError(t, err)

	b, _, err := tmpl.RenderResult(templates.Charger, templates.RenderModeInstance, map[string]any{
		"host": "localhost",
	})
	require.NoError(t, err)

	var instance struct {
		DemandPlan map[string]any
	}
	require.NoError(t, yaml.Unmarshal(b, &instance), string(b))
	require.NotNil(t, instance.DemandPlan, string(b))

	jq, ok := instance.DemandPlan["jq"].(string)
	require.True(t, ok, "jq")

	pipe, err := pipeline.New(util.NewLogger("test"), pipeline.Settings{Jq: jq})
	require.NoError(t, err)

	out, err := pipe.Process(payload)
	require.NoError(t, err)

	rates, err := parseDemandPlan(string(out))
	require.NoError(t, err, string(out))

	return rates, instance.DemandPlan
}

// onekeyPlanStart is the first slot of the test plan, aligned to a quarter hour
var onekeyPlanStart = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

// onekeyPlanPayload builds a onekey/wp/plan payload as the device publishes it: 96 slots of
// 15 min from onekeyPlanStart, published age before now. Without withTs ts_ms is left out.
func onekeyPlanPayload(t *testing.T, age time.Duration, withTs bool) ([]byte, []int) {
	t.Helper()

	var expected, minW, maxW, heating, dhw []int
	for i := range 96 {
		h := 1800 + 9*(i%48) // heating demand rising over half a day
		d := 0
		if i >= 24 && i < 28 {
			d = 1500 // hot water in the afternoon
		}
		heating = append(heating, h)
		dhw = append(dhw, d)
		expected = append(expected, h+d)
		minW = append(minW, d)
		maxW = append(maxW, 4200)
	}

	payload := map[string]any{
		"slot_s":     900,
		"start_ms":   onekeyPlanStart.UnixMilli(),
		"expected_w": expected,
		"min_w":      minW,
		"max_w":      maxW,
		"heating_w":  heating,
		"dhw_w":      dhw,
		"heater_w":   []int{},
	}
	if withTs {
		payload["ts_ms"] = time.Now().Add(-age).UnixMilli()
	}

	b, err := json.Marshal(payload)
	require.NoError(t, err)

	return b, expected
}

// onekeyEmptyPlanPayload builds the payload the device publishes without a valid planner
func onekeyEmptyPlanPayload(t *testing.T) []byte {
	t.Helper()

	b, err := json.Marshal(map[string]any{
		"slot_s":     900,
		"start_ms":   0,
		"expected_w": []int{},
		"min_w":      []int{},
		"max_w":      []int{},
		"heating_w":  []int{},
		"dhw_w":      []int{},
		"heater_w":   []int{},
		"ts_ms":      time.Now().Add(-time.Minute).UnixMilli(),
	})
	require.NoError(t, err)

	return b
}

func TestOneKeyTemplateDemandPlan(t *testing.T) {
	payload, expected := onekeyPlanPayload(t, time.Minute, true)
	rates, cfg := onekeyDemandPlan(t, payload)

	assert.Equal(t, "mqtt", cfg["source"])
	assert.Equal(t, "onekey/wp/plan", cfg["topic"])
	assert.Equal(t, "45m", cfg["timeout"])

	require.Len(t, rates, 96)
	assert.True(t, rates[0].Start.Equal(onekeyPlanStart), "first start")
	assert.True(t, rates[95].End.Equal(onekeyPlanStart.Add(24*time.Hour)), "last end")

	for i, r := range rates {
		assert.Equal(t, 15*time.Minute, r.End.Sub(r.Start), "slot %d", i)
		assert.Equal(t, float64(expected[i]), r.Value, "slot %d", i)
	}
}

// a retained plan the device no longer refreshes must not count
func TestOneKeyTemplateDemandPlanStale(t *testing.T) {
	payload, _ := onekeyPlanPayload(t, 46*time.Minute, true)
	rates, _ := onekeyDemandPlan(t, payload)
	assert.Empty(t, rates, "stale ts_ms")

	payload, _ = onekeyPlanPayload(t, 0, false)
	rates, _ = onekeyDemandPlan(t, payload)
	assert.Empty(t, rates, "missing ts_ms")

	payload, _ = onekeyPlanPayload(t, 44*time.Minute, true)
	rates, _ = onekeyDemandPlan(t, payload)
	assert.Len(t, rates, 96, "ts_ms within 45 min")
}

func TestOneKeyTemplateDemandPlanEmpty(t *testing.T) {
	rates, _ := onekeyDemandPlan(t, onekeyEmptyPlanPayload(t))
	assert.Empty(t, rates)
}

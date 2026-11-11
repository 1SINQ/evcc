package charger

import (
	"encoding/json"
	"os"
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

	// let the background warm-up take the one-time wait
	time.Sleep(50 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := planG()
		done <- err
	}()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, api.ErrOutdated)
	case <-time.After(time.Second):
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
func onekeyDemandPlan(t *testing.T, fixture string) (api.Rates, map[string]any) {
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

	payload, err := os.ReadFile(fixture)
	require.NoError(t, err)

	out, err := pipe.Process(payload)
	require.NoError(t, err)

	rates, err := parseDemandPlan(string(out))
	require.NoError(t, err, string(out))

	return rates, instance.DemandPlan
}

func TestOneKeyTemplateDemandPlan(t *testing.T) {
	rates, cfg := onekeyDemandPlan(t, "testdata/onekey-wp-plan.json")

	assert.Equal(t, "mqtt", cfg["source"])
	assert.Equal(t, "onekey/wp/plan", cfg["topic"])
	assert.Equal(t, "45m", cfg["timeout"])

	var payload struct {
		StartMs   int64     `json:"start_ms"`
		ExpectedW []float64 `json:"expected_w"`
	}
	b, err := os.ReadFile("testdata/onekey-wp-plan.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &payload))

	start := time.UnixMilli(payload.StartMs)

	require.Len(t, rates, 96)
	assert.True(t, rates[0].Start.Equal(start), "first start")
	assert.True(t, rates[95].End.Equal(start.Add(24*time.Hour)), "last end")

	for i, r := range rates {
		assert.Equal(t, 15*time.Minute, r.End.Sub(r.Start), "slot %d", i)
		assert.Equal(t, payload.ExpectedW[i], r.Value, "slot %d", i)
	}
}

func TestOneKeyTemplateDemandPlanEmpty(t *testing.T) {
	rates, _ := onekeyDemandPlan(t, "testdata/onekey-wp-plan-empty.json")
	assert.Empty(t, rates)
}

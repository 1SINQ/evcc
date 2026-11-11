package charger

import (
	"context"
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// without a mode getter the cached mode must follow the last set mode
func TestSgReadyDimCachesMode(t *testing.T) {
	var mode int64
	wb, err := NewSgReady(context.Background(), &embed{}, func(m int64) error {
		mode = m
		return nil
	}, nil, nil)
	require.NoError(t, err)

	require.NoError(t, wb.Dim(true))
	assert.Equal(t, Dim, mode)
	dimmed, err := wb.Dimmed()
	require.NoError(t, err)
	assert.True(t, dimmed)

	require.NoError(t, wb.Dim(false))
	assert.Equal(t, Normal, mode)
	dimmed, err = wb.Dimmed()
	require.NoError(t, err)
	assert.False(t, dimmed)
}

func TestSgReadyDemandPlan(t *testing.T) {
	newSgReady := func(demandplan map[string]any) api.Charger {
		other := map[string]any{
			"setmode": map[string]any{"source": "js", "script": "0"},
		}
		if demandplan != nil {
			other["demandplan"] = demandplan
		}
		wb, err := NewSgReadyFromConfig(t.Context(), other)
		require.NoError(t, err)
		return wb
	}

	_, ok := api.Cap[api.DemandPlanner](newSgReady(nil))
	assert.False(t, ok, "DemandPlanner without demandplan")

	wb := newSgReady(map[string]any{
		"source": "const",
		"value":  `[{"start":"2026-11-10T13:00:00Z","end":"2026-11-10T13:15:00Z","value":2000}]`,
	})
	dp, ok := api.Cap[api.DemandPlanner](wb)
	require.True(t, ok, "DemandPlanner with demandplan")

	rates, err := dp.DemandPlan()
	require.NoError(t, err)
	require.Len(t, rates, 1)
	assert.Equal(t, 2000.0, rates[0].Value)
}

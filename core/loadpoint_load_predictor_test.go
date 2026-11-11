package core

import (
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/api/implement"
	"github.com/evcc-io/evcc/core/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
)

// planCharger is a heating charger publishing its own demand plan
type planCharger struct {
	featureCharger
	implement.Caps
}

func newPlanCharger(fn func() (api.Rates, error), features ...api.Feature) *planCharger {
	c := &planCharger{
		features: features,
		Caps:     implement.New(),
	}
	implement.May(c, implement.DemandPlanner(fn))
	return c
}

func TestDefaultDemandPredictor(t *testing.T) {
	plan := func() (api.Rates, error) { return nil, nil }

	for _, tc := range []struct {
		name      string
		charger   any
		predictor string
		fallback  string
	}{
		{"none", &featureCharger{features: []api.Feature{api.Heating}}, "daily", "daily"},
		{"weekday", &featureCharger{features: []api.Feature{api.Heating, api.DemandWeekday}}, "weekday", "weekday"},
		{"temperature", &featureCharger{features: []api.Feature{api.Heating, api.DemandTemperature}}, "temperature", "temperature"},
		{"plan", newPlanCharger(plan, api.Heating), "plan", "daily"},
		{"plan weekday", newPlanCharger(plan, api.Heating, api.DemandWeekday), "plan", "weekday"},
		{"plan temperature", newPlanCharger(plan, api.Heating, api.DemandTemperature), "plan", "temperature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.predictor, DefaultDemandPredictor(tc.charger))
			assert.Equal(t, tc.fallback, fallbackDemandPredictor(tc.charger))
		})
	}
}

func TestSetDemandPredictor(t *testing.T) {
	lp := NewLoadpoint(util.NewLogger("foo"), settings.NewDatabaseSettingsAdapter("foo"))

	lp.SetDemandPredictor("plan")
	assert.Equal(t, "plan", lp.GetDemandPredictor())

	lp.SetDemandPredictor("bogus")
	assert.Equal(t, "plan", lp.GetDemandPredictor(), "unknown predictor is ignored")

	lp.SetDemandPredictor("")
	assert.Equal(t, "", lp.GetDemandPredictor())
}

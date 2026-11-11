package charger

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/plugin"
)

// demandPlanGetter creates a getter for the demand plan of a heating device. The plugin
// must provide a JSON string in api.Rates form with the expected power in W.
// A nil config returns a nil getter.
func demandPlanGetter(ctx context.Context, cfg *plugin.Config) (func() (api.Rates, error), error) {
	g, err := cfg.StringGetter(ctx)
	if g == nil || err != nil {
		return nil, err
	}

	return newDemandPlanGetter(g), nil
}

// newDemandPlanGetter wraps a string getter into a demand plan getter.
// Monitored sources with timeout (mqtt) block their very first read until a value
// arrives or the timeout expires. A plan may never arrive, so that one-time wait is
// taken in the background instead of by the optimizer; later reads return at once.
func newDemandPlanGetter(g func() (string, error)) func() (api.Rates, error) {
	go func() { _, _ = g() }()

	return func() (api.Rates, error) {
		s, err := g()
		if err != nil {
			return nil, err
		}
		return parseDemandPlan(s)
	}
}

// parseDemandPlan parses a demand plan in api.Rates JSON form and sorts it by start
func parseDemandPlan(s string) (api.Rates, error) {
	var res api.Rates
	if err := json.Unmarshal([]byte(s), &res); err != nil {
		return nil, fmt.Errorf("demand plan: %w", err)
	}

	res.Sort()

	return res, nil
}

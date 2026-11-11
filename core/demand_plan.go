package core

import (
	"math"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/tariff"
)

// demandPlanEnergy converts a demand plan (W) into energy in kWh for n 15min slots starting at start.
// Each slot takes the rate valid at its start, finer grained rates are sampled, not averaged.
// It returns the energies and the number of slots covered by the plan, contiguous from slot 0.
// Coverage ends at the first slot without a rate or with an invalid (non-finite or negative) value.
func demandPlanEnergy(rr api.Rates, start time.Time, n int) ([]float64, int) {
	res := make([]float64, n)

	for i := range n {
		r, err := rr.At(start.Add(time.Duration(i) * tariff.SlotDuration))
		if err != nil || math.IsNaN(r.Value) || math.IsInf(r.Value, 0) || r.Value < 0 {
			return res, i
		}

		res[i] = r.Value / 1e3 * tariff.SlotDuration.Hours()
	}

	return res, n
}

// demandPlanProfile returns the demand plan of a heating loadpoint in kWh for minLen slots
// starting at the current slot and the number of covered slots. It is only available if
// the schedule predictor is active and the charger provides a demand plan.
func (lp *Loadpoint) demandPlanProfile(minLen int) ([]float64, int) {
	if lp.activeDemandPredictor() != "schedule" {
		return nil, 0
	}

	dp, ok := api.Cap[api.DemandPlanner](lp.charger)
	if !ok {
		return nil, 0
	}

	rr, err := dp.DemandPlan()
	if err != nil {
		lp.log.DEBUG.Printf("demand plan: %v", err)
		return nil, 0
	}

	res, covered := demandPlanEnergy(rr, lp.clock.Now().Truncate(tariff.SlotDuration), minLen)
	if covered == 0 {
		lp.log.DEBUG.Println("demand plan: no current slots")
	}

	return res, covered
}

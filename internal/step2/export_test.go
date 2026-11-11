//go:build install && step2 && devtest

package step2

import (
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/plan"
)

// DataHookForTest is the data hook of a session on e (the hook's own guards,
// reached without the executor's step order).
func DataHookForTest(e *device.Env, o Options) (func(op string, d *plan.DataPlan) error, func(), error) {
	s, err := open(e, o)
	if err != nil {
		return nil, nil, err
	}
	return s.dataHook(o.DataRunner), s.close, nil
}

// StopHoldForTest runs the STOP of the service with the error err.
func StopHoldForTest(e *device.Env, o Options, se ServiceEnv, err error) (*ServiceResult, error) {
	return stopHold(e, o, se, nil, err)
}

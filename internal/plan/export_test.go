package plan

// AllowDryRunFinish lets a test finish a step-2 plan of a dry run (review
// T4-2 NIT-4: the switch is not an exported field of the release package).
func AllowDryRunFinish(in *FinishInput) { in.allowDryRunPlan = true }

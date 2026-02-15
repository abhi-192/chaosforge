package model

// RunReport is a simple persisted report artifact.
// It is written to S3 by the reporter function and can be used for audits.
type RunReport struct {
	Run    ChaosRun        `json:"run"`
	Events []ChaosRunEvent `json:"events"`
}

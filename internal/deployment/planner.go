package deployment

import "deployment-agent/internal/discovery"

// Step is one image whose remote digest differs from what is running.
type Step struct {
	Key         string
	ContainerID string
	Name        string
	Service     string
	Image       string
	FromDigest  string
	ToDigest    string
}

// Plan is the set of changes for one pass.
type Plan struct {
	Steps []Step
}

// Build diffs candidates against resolved remote digests.
// Pinned images, local images, stopped containers, and rejected digests are omitted.
func Build(candidates []discovery.Candidate, remote, rejected map[string]string) Plan {
	var plan Plan
	for _, cand := range candidates {
		if cand.Pinned || cand.LocalOnly || cand.Stopped || cand.IsAgent {
			continue
		}
		digest := remote[cand.Key]
		if digest == "" || digest == cand.CurrentDigest || rejected[cand.Key] == digest {
			continue
		}
		plan.Steps = append(plan.Steps, Step{
			Key:         cand.Key,
			ContainerID: cand.ContainerID,
			Name:        cand.Name,
			Service:     cand.Service,
			Image:       cand.Image,
			FromDigest:  cand.CurrentDigest,
			ToDigest:    digest,
		})
	}
	return plan
}

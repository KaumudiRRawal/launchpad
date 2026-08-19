package store

import (
	"context"
	"fmt"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

// ResolveUpstream finds where traffic for a subdomain should go.
//
// The LEFT JOIN LATERAL is what lets one query distinguish the two failure
// modes the proxy has to answer differently: a subdomain nobody has ever
// minted is a 404, while an environment that exists but has nothing serving
// yet is a 503 the caller should retry. A plain inner join would collapse both
// into "no rows".
func (r *Repository) ResolveUpstream(ctx context.Context, subdomain string) (domain.Upstream, error) {
	const query = `
		SELECT e.id, live.id, live.internal_url
		FROM environments e
		LEFT JOIN LATERAL (
			SELECT d.id, d.internal_url
			FROM deployments d
			WHERE d.environment_id = e.id
			  AND d.status = 'live'
			  AND d.internal_url IS NOT NULL
			ORDER BY d.completed_at DESC
			LIMIT 1
		) live ON true
		WHERE e.subdomain = $1`

	var environmentID string
	var deploymentID, internalURL *string

	err := r.pool.QueryRow(ctx, query, subdomain).Scan(&environmentID, &deploymentID, &internalURL)
	if err != nil {
		return domain.Upstream{}, translate(err)
	}
	if deploymentID == nil || internalURL == nil {
		return domain.Upstream{}, fmt.Errorf("environment %q: %w", subdomain, domain.ErrNoLiveDeployment)
	}

	return domain.Upstream{
		EnvironmentID: environmentID,
		DeploymentID:  *deploymentID,
		URL:           *internalURL,
	}, nil
}

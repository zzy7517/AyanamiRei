package featureflags

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// Clients gate the task page's trees section and the Code settings tab on
// this key, read from GET /api/config. A key the server evaluates but never
// publishes reads as off in every client, whatever the deployment set, and
// the features stay unreachable.
func TestEvaluateFrontendPublicFlagsPublishesTaskSessions(t *testing.T) {
	provider := featureflag.NewStaticProvider()
	provider.Set(TaskSessionsV1, featureflag.Rule{Default: true})
	on := EvaluateFrontendPublicFlags(context.Background(), featureflag.NewService(provider))

	// Off is published too, as false: a missing key is what tells a client
	// that its server predates the feature.
	off := EvaluateFrontendPublicFlags(context.Background(), featureflag.NewService(featureflag.NewStaticProvider()))

	for _, key := range []string{TaskSessionsV1} {
		if value, ok := on[key]; !ok || !value {
			t.Errorf("%s on: published = %v, present = %v, want true", key, value, ok)
		}
		if value, ok := off[key]; !ok || value {
			t.Errorf("%s off: published = %v, present = %v, want false", key, value, ok)
		}
	}
}

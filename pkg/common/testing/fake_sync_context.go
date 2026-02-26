package testing

import (
	"testing"

	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/events/eventstesting"
	"k8s.io/client-go/util/workqueue"
)

type FakeSyncContext struct {
	spokeName string
	recorder  events.Recorder
	queue     workqueue.RateLimitingInterface //nolint:staticcheck // SA1019 keeping deprecated type for backward compatibility
}

func (f FakeSyncContext) Queue() workqueue.RateLimitingInterface { return f.queue } //nolint:staticcheck
func (f FakeSyncContext) QueueKey() string                       { return f.spokeName }
func (f FakeSyncContext) Recorder() events.Recorder              { return f.recorder }

func NewFakeSyncContext(t *testing.T, clusterName string) *FakeSyncContext {
	return &FakeSyncContext{
		spokeName: clusterName,
		recorder:  eventstesting.NewTestingEventRecorder(t),
		queue:     workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()), //nolint:staticcheck
	}
}

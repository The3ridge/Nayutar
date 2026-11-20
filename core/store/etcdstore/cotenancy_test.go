//go:build etcd

package etcdstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store/storetest"
)

func TestCotenancyMeasurement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	report, err := storetest.MeasureCotenancy(ctx, isolated(t), storetest.CotenancyLoad{
		Duration:      2 * time.Second,
		StrongRate:    50,
		HotWriters:    8,
		HotKeys:       64,
		HotValueBytes: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The test cluster runs without fsync on loopback, so absolute values say nothing about a real deployment.
	t.Log("\n" + report.String())
}

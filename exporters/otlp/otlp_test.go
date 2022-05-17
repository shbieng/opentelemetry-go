// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package otlp_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/exporters/otlp"
	metricpb "go.opentelemetry.io/otel/exporters/otlp/internal/opentelemetry-proto-gen/metrics/v1"
	tracepb "go.opentelemetry.io/otel/exporters/otlp/internal/opentelemetry-proto-gen/trace/v1"
	"go.opentelemetry.io/otel/exporters/otlp/internal/transform"

	metricsdk "go.opentelemetry.io/otel/sdk/export/metric"
	tracesdk "go.opentelemetry.io/otel/sdk/export/trace"
)

type stubProtocolDriver struct {
	started         int
	stopped         int
	tracesExported  int
	metricsExported int

	injectedStartError error
	injectedStopError  error

	rm []metricpb.ResourceMetrics
	rs []tracepb.ResourceSpans
}

var _ otlp.ProtocolDriver = (*stubProtocolDriver)(nil)

func (m *stubProtocolDriver) Start(ctx context.Context) error {
	m.started++
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return m.injectedStartError
	}
}

func (m *stubProtocolDriver) Stop(ctx context.Context) error {
	m.stopped++
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return m.injectedStopError
	}
}

func (m *stubProtocolDriver) ExportMetrics(parent context.Context, cps metricsdk.CheckpointSet, selector metricsdk.ExportKindSelector) error {
	m.metricsExported++
	rms, err := transform.CheckpointSet(parent, selector, cps, 1)
	if err != nil {
		return err
	}
	for _, rm := range rms {
		if rm == nil {
			continue
		}
		m.rm = append(m.rm, *rm)
	}
	return nil
}

func (m *stubProtocolDriver) ExportTraces(ctx context.Context, ss []*tracesdk.SpanSnapshot) error {
	m.tracesExported++
	for _, rs := range transform.SpanData(ss) {
		if rs == nil {
			continue
		}
		m.rs = append(m.rs, *rs)
	}
	return nil
}

func (m *stubProtocolDriver) Reset() {
	m.rm = nil
	m.rs = nil
}

func newExporter(t *testing.T, opts ...otlp.ExporterOption) (*otlp.Exporter, *stubProtocolDriver) {
	driver := &stubProtocolDriver{}
	exp, err := otlp.NewExporter(context.Background(), driver, opts...)
	require.NoError(t, err)
	return exp, driver
}

func TestExporterShutdownHonorsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	e := otlp.NewUnstartedExporter(&stubProtocolDriver{})
	if err := e.Start(ctx); err != nil {
		t.Fatalf("failed to start exporter: %v", err)
	}

	innerCtx, innerCancel := context.WithTimeout(ctx, time.Microsecond)
	<-time.After(time.Second)
	if err := e.Shutdown(innerCtx); err == nil {
		t.Error("expected context DeadlineExceeded error, got nil")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context DeadlineExceeded error, got %v", err)
	}
	innerCancel()
}

func TestExporterShutdownHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	e := otlp.NewUnstartedExporter(&stubProtocolDriver{})
	if err := e.Start(ctx); err != nil {
		t.Fatalf("failed to start exporter: %v", err)
	}

	var innerCancel context.CancelFunc
	ctx, innerCancel = context.WithCancel(ctx)
	innerCancel()
	if err := e.Shutdown(ctx); err == nil {
		t.Error("expected context canceled error, got nil")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context canceled error, got %v", err)
	}
}

func TestExporterShutdownNoError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	e := otlp.NewUnstartedExporter(&stubProtocolDriver{})
	if err := e.Start(ctx); err != nil {
		t.Fatalf("failed to start exporter: %v", err)
	}

	if err := e.Shutdown(ctx); err != nil {
		t.Errorf("shutdown errored: expected nil, got %v", err)
	}
}

func TestExporterShutdownManyTimes(t *testing.T) {
	ctx := context.Background()
	e, err := otlp.NewExporter(ctx, &stubProtocolDriver{})
	if err != nil {
		t.Fatalf("failed to start an exporter: %v", err)
	}
	ch := make(chan struct{})
	wg := sync.WaitGroup{}
	const num int = 20
	wg.Add(num)
	errs := make([]error, num)
	for i := 0; i < num; i++ {
		go func(idx int) {
			defer wg.Done()
			<-ch
			errs[idx] = e.Shutdown(ctx)
		}(i)
	}
	close(ch)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("failed to shutdown exporter: %v", err)
		}
	}
}

func TestSplitDriver(t *testing.T) {
	driverTraces := &stubProtocolDriver{}
	driverMetrics := &stubProtocolDriver{}
	config := otlp.SplitConfig{
		ForMetrics: driverMetrics,
		ForTraces:  driverTraces,
	}
	driver := otlp.NewSplitDriver(config)
	ctx := context.Background()
	assert.NoError(t, driver.Start(ctx))
	assert.Equal(t, 1, driverTraces.started)
	assert.Equal(t, 1, driverMetrics.started)
	assert.Equal(t, 0, driverTraces.stopped)
	assert.Equal(t, 0, driverMetrics.stopped)
	assert.Equal(t, 0, driverTraces.tracesExported)
	assert.Equal(t, 0, driverTraces.metricsExported)
	assert.Equal(t, 0, driverMetrics.tracesExported)
	assert.Equal(t, 0, driverMetrics.metricsExported)

	assert.NoError(t, driver.ExportMetrics(ctx, discCheckpointSet{}, metricsdk.StatelessExportKindSelector()))
	assert.NoError(t, driver.ExportTraces(ctx, []*tracesdk.SpanSnapshot{discSpanSnapshot()}))
	assert.Len(t, driverTraces.rm, 0)
	assert.Len(t, driverTraces.rs, 1)
	assert.Len(t, driverMetrics.rm, 1)
	assert.Len(t, driverMetrics.rs, 0)
	assert.Equal(t, 1, driverTraces.tracesExported)
	assert.Equal(t, 0, driverTraces.metricsExported)
	assert.Equal(t, 0, driverMetrics.tracesExported)
	assert.Equal(t, 1, driverMetrics.metricsExported)

	assert.NoError(t, driver.Stop(ctx))
	assert.Equal(t, 1, driverTraces.started)
	assert.Equal(t, 1, driverMetrics.started)
	assert.Equal(t, 1, driverTraces.stopped)
	assert.Equal(t, 1, driverMetrics.stopped)
	assert.Equal(t, 1, driverTraces.tracesExported)
	assert.Equal(t, 0, driverTraces.metricsExported)
	assert.Equal(t, 0, driverMetrics.tracesExported)
	assert.Equal(t, 1, driverMetrics.metricsExported)
}

func TestSplitDriverFail(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < 16; i++ {
		var (
			errStartMetric error
			errStartTrace  error
			errStopMetric  error
			errStopTrace   error
		)
		if (i & 1) != 0 {
			errStartTrace = errors.New("trace start failed")
		}
		if (i & 2) != 0 {
			errStopTrace = errors.New("trace stop failed")
		}
		if (i & 4) != 0 {
			errStartMetric = errors.New("metric start failed")
		}
		if (i & 8) != 0 {
			errStopMetric = errors.New("metric stop failed")
		}
		shouldStartFail := errStartTrace != nil || errStartMetric != nil
		shouldStopFail := errStopTrace != nil || errStopMetric != nil

		driverTraces := &stubProtocolDriver{
			injectedStartError: errStartTrace,
			injectedStopError:  errStopTrace,
		}
		driverMetrics := &stubProtocolDriver{
			injectedStartError: errStartMetric,
			injectedStopError:  errStopMetric,
		}
		config := otlp.SplitConfig{
			ForMetrics: driverMetrics,
			ForTraces:  driverTraces,
		}
		driver := otlp.NewSplitDriver(config)
		errStart := driver.Start(ctx)
		if shouldStartFail {
			assert.Error(t, errStart)
		} else {
			assert.NoError(t, errStart)
		}
		errStop := driver.Stop(ctx)
		if shouldStopFail {
			assert.Error(t, errStop)
		} else {
			assert.NoError(t, errStop)
		}
	}
}

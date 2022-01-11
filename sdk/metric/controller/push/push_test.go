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

package push_test

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/metric"
	"go.opentelemetry.io/otel/label"
	export "go.opentelemetry.io/otel/sdk/export/metric"
	"go.opentelemetry.io/otel/sdk/export/metric/aggregation"
	"go.opentelemetry.io/otel/sdk/metric/controller/controllertest"
	"go.opentelemetry.io/otel/sdk/metric/controller/push"
	"go.opentelemetry.io/otel/sdk/metric/processor/basic"
	processorTest "go.opentelemetry.io/otel/sdk/metric/processor/processortest"
	"go.opentelemetry.io/otel/sdk/resource"
)

var testResource = resource.New(label.String("R", "V"))

type handler struct {
	sync.Mutex
	err error
}

func (h *handler) Handle(err error) {
	h.Lock()
	h.err = err
	h.Unlock()
}

func (h *handler) Flush() error {
	h.Lock()
	err := h.err
	h.err = nil
	h.Unlock()
	return err
}

var testHandler *handler

func init() {
	testHandler = new(handler)
	global.SetErrorHandler(testHandler)
}

func newExporter() *processorTest.Exporter {
	return processorTest.NewExporter(
		export.PassThroughExporter,
		label.DefaultEncoder(),
	)
}

func newCheckpointer() export.Checkpointer {
	return processorTest.Checkpointer(
		processorTest.NewProcessor(
			processorTest.AggregatorSelector(),
			label.DefaultEncoder(),
		),
	)
}

func TestPushDoubleStop(t *testing.T) {
	exporter := newExporter()
	checkpointer := newCheckpointer()
	p := push.New(checkpointer, exporter)
	p.Start()
	p.Stop()
	p.Stop()
}

func TestPushDoubleStart(t *testing.T) {
	exporter := newExporter()
	checkpointer := newCheckpointer()
	p := push.New(checkpointer, exporter)
	p.Start()
	p.Start()
	p.Stop()
}

func TestPushTicker(t *testing.T) {
	exporter := newExporter()
	checkpointer := newCheckpointer()
	p := push.New(
		checkpointer,
		exporter,
		push.WithPeriod(time.Second),
		push.WithResource(testResource),
	)
	meter := p.Provider().Meter("name")

	mock := controllertest.NewMockClock()
	p.SetClock(mock)

	ctx := context.Background()

	counter := metric.Must(meter).NewInt64Counter("counter.sum")

	p.Start()

	counter.Add(ctx, 3)

	require.EqualValues(t, map[string]float64{}, exporter.Values())

	mock.Add(time.Second)
	runtime.Gosched()

	require.EqualValues(t, map[string]float64{
		"counter.sum//R=V": 3,
	}, exporter.Values())

	require.Equal(t, 1, exporter.ExportCount())
	exporter.Reset()

	counter.Add(ctx, 7)

	mock.Add(time.Second)
	runtime.Gosched()

	require.EqualValues(t, map[string]float64{
		"counter.sum//R=V": 10,
	}, exporter.Values())

	require.Equal(t, 1, exporter.ExportCount())
	exporter.Reset()

	p.Stop()
}

func TestPushExportError(t *testing.T) {
	injector := func(name string, e error) func(r export.Record) error {
		return func(r export.Record) error {
			if r.Descriptor().Name() == name {
				return e
			}
			return nil
		}
	}
	var errAggregator = fmt.Errorf("unexpected error")
	var tests = []struct {
		name          string
		injectedError error
		expected      map[string]float64
		expectedError error
	}{
		{"errNone", nil, map[string]float64{
			"counter1.sum/X=Y/R=V": 3,
			"counter2.sum//R=V":    5,
		}, nil},
		{"errNoData", aggregation.ErrNoData, map[string]float64{
			"counter2.sum//R=V": 5,
		}, nil},
		{"errUnexpected", errAggregator, map[string]float64{}, errAggregator},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter := newExporter()
			exporter.InjectErr = injector("counter1.sum", tt.injectedError)

			// This test validates the error handling
			// behavior of the basic Processor is honored
			// by the push processor.
			checkpointer := basic.New(processorTest.AggregatorSelector(), exporter)
			p := push.New(
				checkpointer,
				exporter,
				push.WithPeriod(time.Second),
				push.WithResource(testResource),
			)

			mock := controllertest.NewMockClock()
			p.SetClock(mock)

			ctx := context.Background()

			meter := p.Provider().Meter("name")
			counter1 := metric.Must(meter).NewInt64Counter("counter1.sum")
			counter2 := metric.Must(meter).NewInt64Counter("counter2.sum")

			p.Start()
			runtime.Gosched()

			counter1.Add(ctx, 3, label.String("X", "Y"))
			counter2.Add(ctx, 5)

			require.Equal(t, 0, exporter.ExportCount())
			require.Nil(t, testHandler.Flush())

			mock.Add(time.Second)
			runtime.Gosched()

			require.Equal(t, 1, exporter.ExportCount())
			if tt.expectedError == nil {
				require.EqualValues(t, tt.expected, exporter.Values())
				require.NoError(t, testHandler.Flush())
			} else {
				err := testHandler.Flush()
				require.Error(t, err)
				require.Equal(t, tt.expectedError, err)
			}

			p.Stop()
		})
	}
}

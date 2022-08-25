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

package otlphttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	jsonpb "google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/exporters/otlp/internal/transform"
	metricsdk "go.opentelemetry.io/otel/sdk/export/metric"
	tracesdk "go.opentelemetry.io/otel/sdk/export/trace"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

const contentTypeProto = "application/x-protobuf"
const contentTypeJSON = "application/json"

// Keep it in sync with golang's DefaultTransport from net/http! We
// have our own copy to avoid handling a situation where the
// DefaultTransport is overwritten with some different implementation
// of http.RoundTripper or it's modified by other package.
var ourTransport *http.Transport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		DualStack: true,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

type driver struct {
	metricsDriver signalDriver
	tracesDriver  signalDriver
	cfg           config

	stopCh chan struct{}
}

type signalDriver struct {
	cfg        signalConfig
	generalCfg config
	client     *http.Client
	stopCh     chan struct{}
}

var _ otlp.ProtocolDriver = (*driver)(nil)

// NewDriver creates a new HTTP driver.
func NewDriver(opts ...Option) otlp.ProtocolDriver {
	cfg := newDefaultConfig()
	applyEnvConfigs(&cfg, os.Getenv)

	for _, opt := range opts {
		opt.Apply(&cfg)
	}
	for pathPtr, defaultPath := range map[*string]string{
		&cfg.traces.urlPath:  DefaultTracesPath,
		&cfg.metrics.urlPath: DefaultMetricsPath,
	} {
		tmp := strings.TrimSpace(*pathPtr)
		if tmp == "" {
			tmp = defaultPath
		} else {
			tmp = path.Clean(tmp)
			if !path.IsAbs(tmp) {
				tmp = fmt.Sprintf("/%s", tmp)
			}
		}
		*pathPtr = tmp
	}
	if cfg.maxAttempts <= 0 {
		cfg.maxAttempts = DefaultMaxAttempts
	}
	if cfg.maxAttempts > DefaultMaxAttempts {
		cfg.maxAttempts = DefaultMaxAttempts
	}
	if cfg.backoff <= 0 {
		cfg.backoff = DefaultBackoff
	}

	metricsClient := &http.Client{
		Transport: ourTransport,
		Timeout:   cfg.metrics.timeout,
	}
	if cfg.metrics.tlsCfg != nil {
		transport := ourTransport.Clone()
		transport.TLSClientConfig = cfg.metrics.tlsCfg
		metricsClient.Transport = transport
	}

	tracesClient := &http.Client{
		Transport: ourTransport,
		Timeout:   cfg.traces.timeout,
	}
	if cfg.traces.tlsCfg != nil {
		transport := ourTransport.Clone()
		transport.TLSClientConfig = cfg.traces.tlsCfg
		tracesClient.Transport = transport
	}

	stopCh := make(chan struct{})
	return &driver{
		tracesDriver: signalDriver{
			cfg:        cfg.traces,
			generalCfg: cfg,
			stopCh:     stopCh,
			client:     tracesClient,
		},
		metricsDriver: signalDriver{
			cfg:        cfg.metrics,
			generalCfg: cfg,
			stopCh:     stopCh,
			client:     metricsClient,
		},
		cfg:    cfg,
		stopCh: stopCh,
	}
}

// Start implements otlp.ProtocolDriver.
func (d *driver) Start(ctx context.Context) error {
	// nothing to do
	return nil
}

// Stop implements otlp.ProtocolDriver.
func (d *driver) Stop(ctx context.Context) error {
	close(d.stopCh)
	return nil
}

// ExportMetrics implements otlp.ProtocolDriver.
func (d *driver) ExportMetrics(ctx context.Context, cps metricsdk.CheckpointSet, selector metricsdk.ExportKindSelector) error {
	rms, err := transform.CheckpointSet(ctx, selector, cps, 1)
	if err != nil {
		return err
	}
	if len(rms) == 0 {
		return nil
	}
	pbRequest := &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: rms,
	}
	rawRequest, err := d.marshal(pbRequest)
	if err != nil {
		return err
	}
	return d.metricsDriver.send(ctx, rawRequest)
}

// ExportTraces implements otlp.ProtocolDriver.
func (d *driver) ExportTraces(ctx context.Context, ss []*tracesdk.SpanSnapshot) error {
	protoSpans := transform.SpanData(ss)
	if len(protoSpans) == 0 {
		return nil
	}
	pbRequest := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: protoSpans,
	}
	rawRequest, err := d.marshal(pbRequest)
	if err != nil {
		return err
	}
	return d.tracesDriver.send(ctx, rawRequest)
}

func (d *driver) marshal(msg proto.Message) ([]byte, error) {
	if d.cfg.marshaler == MarshalJSON {
		return jsonpb.Marshal(msg)
	}
	return proto.Marshal(msg)
}

func (d *signalDriver) send(ctx context.Context, rawRequest []byte) error {
	address := fmt.Sprintf("%s://%s%s", d.getScheme(), d.cfg.endpoint, d.cfg.urlPath)
	var cancel context.CancelFunc
	ctx, cancel = d.contextWithStop(ctx)
	defer cancel()
	for i := 0; i < d.generalCfg.maxAttempts; i++ {
		response, err := d.singleSend(ctx, rawRequest, address)
		if err != nil {
			return err
		}
		// We don't care about the body, so try to read it
		// into /dev/null and close it immediately. The
		// reading part is to facilitate connection reuse.
		_, _ = io.Copy(ioutil.Discard, response.Body)
		_ = response.Body.Close()
		switch response.StatusCode {
		case http.StatusOK:
			return nil
		case http.StatusTooManyRequests:
			fallthrough
		case http.StatusServiceUnavailable:
			select {
			case <-time.After(getWaitDuration(d.generalCfg.backoff, i)):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			return fmt.Errorf("failed with HTTP status %s", response.Status)
		}
	}
	return fmt.Errorf("failed to send data to %s after %d tries", address, d.generalCfg.maxAttempts)
}

func (d *signalDriver) getScheme() string {
	if d.cfg.insecure {
		return "http"
	}
	return "https"
}

func getWaitDuration(backoff time.Duration, i int) time.Duration {
	// Strategy: after nth failed attempt, attempt resending after
	// k * initialBackoff + jitter, where k is a random number in
	// range [0, 2^n-1), and jitter is a random percentage of
	// initialBackoff from [-5%, 5%).
	//
	// Based on
	// https://en.wikipedia.org/wiki/Exponential_backoff#Example_exponential_backoff_algorithm
	//
	// Jitter is our addition.

	// There won't be an overflow, since i is capped to
	// DefaultMaxAttempts (5).
	upperK := (int64)(1) << (i + 1)
	jitterPercent := (rand.Float64() - 0.5) / 10.
	jitter := jitterPercent * (float64)(backoff)
	k := rand.Int63n(upperK)
	return (time.Duration)(k)*backoff + (time.Duration)(jitter)
}

func (d *signalDriver) contextWithStop(ctx context.Context) (context.Context, context.CancelFunc) {
	// Unify the parent context Done signal with the driver's stop
	// channel.
	ctx, cancel := context.WithCancel(ctx)
	go func(ctx context.Context, cancel context.CancelFunc) {
		select {
		case <-ctx.Done():
			// Nothing to do, either cancelled or deadline
			// happened.
		case <-d.stopCh:
			cancel()
		}
	}(ctx, cancel)
	return ctx, cancel
}

func (d *signalDriver) singleSend(ctx context.Context, rawRequest []byte, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, nil)
	if err != nil {
		return nil, err
	}
	bodyReader, contentLength, headers := d.prepareBody(rawRequest)
	// Not closing bodyReader through defer, the HTTP Client's
	// Transport will do it for us
	request.Body = bodyReader
	request.ContentLength = contentLength
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	return d.client.Do(request)
}

func (d *signalDriver) prepareBody(rawRequest []byte) (io.ReadCloser, int64, http.Header) {
	var bodyReader io.ReadCloser
	headers := http.Header{}
	for k, v := range d.cfg.headers {
		headers.Set(k, v)
	}
	contentLength := (int64)(len(rawRequest))
	if d.generalCfg.marshaler == MarshalJSON {
		headers.Set("Content-Type", contentTypeJSON)
	} else {
		headers.Set("Content-Type", contentTypeProto)
	}
	requestReader := bytes.NewBuffer(rawRequest)
	switch d.cfg.compression {
	case NoCompression:
		bodyReader = ioutil.NopCloser(requestReader)
	case GzipCompression:
		preader, pwriter := io.Pipe()
		go func() {
			defer pwriter.Close()
			gzipper := gzip.NewWriter(pwriter)
			defer gzipper.Close()
			_, err := io.Copy(gzipper, requestReader)
			if err != nil {
				otel.Handle(fmt.Errorf("otlphttp: failed to gzip request: %v", err))
			}
		}()
		headers.Set("Content-Encoding", "gzip")
		bodyReader = preader
		contentLength = -1
	}
	return bodyReader, contentLength, headers
}

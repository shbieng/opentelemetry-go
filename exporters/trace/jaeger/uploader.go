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

package jaeger // import "go.opentelemetry.io/otel/exporters/trace/jaeger"

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/exporters/trace/jaeger/internal/third_party/thrift/lib/go/thrift"

	gen "go.opentelemetry.io/otel/exporters/trace/jaeger/internal/gen-go/jaeger"
)

// batchUploader send a batch of spans to Jaeger
type batchUploader interface {
	upload(batch *gen.Batch) error
}

type EndpointOption func() (batchUploader, error)

// WithAgentEndpoint configures the Jaeger exporter to send spans to a jaeger-agent. This will
// use the following environment variables for configuration if no explicit option is provided:
//
// - OTEL_EXPORTER_JAEGER_AGENT_HOST is used for the agent address host
// - OTEL_EXPORTER_JAEGER_AGENT_PORT is used for the agent address port
//
// The passed options will take precedence over any environment variables and default values
// will be used if neither are provided.
func WithAgentEndpoint(options ...AgentEndpointOption) EndpointOption {
	return func() (batchUploader, error) {
		o := &AgentEndpointOptions{
			agentClientUDPParams{
				AttemptReconnecting: true,
				Host:                envOr(envAgentHost, "localhost"),
				Port:                envOr(envAgentPort, "6832"),
			},
		}
		for _, opt := range options {
			opt(o)
		}

		client, err := newAgentClientUDP(o.agentClientUDPParams)
		if err != nil {
			return nil, err
		}

		return &agentUploader{client: client}, nil
	}
}

type AgentEndpointOption func(o *AgentEndpointOptions)

type AgentEndpointOptions struct {
	agentClientUDPParams
}

// WithAgentHost sets a host to be used in the agent client endpoint.
// This option overrides any value set for the
// OTEL_EXPORTER_JAEGER_AGENT_HOST environment variable.
// If this option is not passed and the env var is not set, "localhost" will be used by default.
func WithAgentHost(host string) AgentEndpointOption {
	return func(o *AgentEndpointOptions) {
		o.Host = host
	}
}

// WithAgentPort sets a port to be used in the agent client endpoint.
// This option overrides any value set for the
// OTEL_EXPORTER_JAEGER_AGENT_PORT environment variable.
// If this option is not passed and the env var is not set, "6832" will be used by default.
func WithAgentPort(port string) AgentEndpointOption {
	return func(o *AgentEndpointOptions) {
		o.Port = port
	}
}

// WithLogger sets a logger to be used by agent client.
func WithLogger(logger *log.Logger) AgentEndpointOption {
	return func(o *AgentEndpointOptions) {
		o.Logger = logger
	}
}

// WithDisableAttemptReconnecting sets option to disable reconnecting udp client.
func WithDisableAttemptReconnecting() AgentEndpointOption {
	return func(o *AgentEndpointOptions) {
		o.AttemptReconnecting = false
	}
}

// WithAttemptReconnectingInterval sets the interval between attempts to re resolve agent endpoint.
func WithAttemptReconnectingInterval(interval time.Duration) AgentEndpointOption {
	return func(o *AgentEndpointOptions) {
		o.AttemptReconnectInterval = interval
	}
}

// WithCollectorEndpoint defines the full url to the Jaeger HTTP Thrift collector.
// For example, http://localhost:14268/api/traces
func WithCollectorEndpoint(collectorEndpoint string, options ...CollectorEndpointOption) EndpointOption {
	return func() (batchUploader, error) {
		// Overwrite collector endpoint if environment variables are available.
		if e := CollectorEndpointFromEnv(); e != "" {
			collectorEndpoint = e
		}

		if collectorEndpoint == "" {
			return nil, errors.New("collectorEndpoint must not be empty")
		}

		o := &CollectorEndpointOptions{
			httpClient: http.DefaultClient,
		}

		options = append(options, WithCollectorEndpointOptionFromEnv())
		for _, opt := range options {
			opt(o)
		}

		return &collectorUploader{
			endpoint:   collectorEndpoint,
			username:   o.username,
			password:   o.password,
			httpClient: o.httpClient,
		}, nil
	}
}

type CollectorEndpointOption func(o *CollectorEndpointOptions)

type CollectorEndpointOptions struct {
	// username to be used if basic auth is required.
	username string

	// password to be used if basic auth is required.
	password string

	// httpClient to be used to make requests to the collector endpoint.
	httpClient *http.Client
}

// WithUsername sets the username to be used if basic auth is required.
func WithUsername(username string) CollectorEndpointOption {
	return func(o *CollectorEndpointOptions) {
		o.username = username
	}
}

// WithPassword sets the password to be used if basic auth is required.
func WithPassword(password string) CollectorEndpointOption {
	return func(o *CollectorEndpointOptions) {
		o.password = password
	}
}

// WithHTTPClient sets the http client to be used to make request to the collector endpoint.
func WithHTTPClient(client *http.Client) CollectorEndpointOption {
	return func(o *CollectorEndpointOptions) {
		o.httpClient = client
	}
}

// agentUploader implements batchUploader interface sending batches to
// Jaeger through the UDP agent.
type agentUploader struct {
	client *agentClientUDP
}

var _ batchUploader = (*agentUploader)(nil)

func (a *agentUploader) upload(batch *gen.Batch) error {
	return a.client.EmitBatch(batch)
}

// collectorUploader implements batchUploader interface sending batches to
// Jaeger through the collector http endpoint.
type collectorUploader struct {
	endpoint   string
	username   string
	password   string
	httpClient *http.Client
}

var _ batchUploader = (*collectorUploader)(nil)

func (c *collectorUploader) upload(batch *gen.Batch) error {
	body, err := serialize(batch)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", c.endpoint, body)
	if err != nil {
		return err
	}
	if c.username != "" && c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	req.Header.Set("Content-Type", "application/x-thrift")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}

	_, _ = io.Copy(ioutil.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("failed to upload traces; HTTP status code: %d", resp.StatusCode)
	}
	return nil
}

func serialize(obj thrift.TStruct) (*bytes.Buffer, error) {
	buf := thrift.NewTMemoryBuffer()
	if err := obj.Write(context.Background(), thrift.NewTBinaryProtocolConf(buf, &thrift.TConfiguration{})); err != nil {
		return nil, err
	}
	return buf.Buffer, nil
}

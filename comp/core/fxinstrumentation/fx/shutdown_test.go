// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package fx

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/config/model"
	pb "github.com/DataDog/datadog-agent/pkg/proto/pbgo/trace"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type shutdownRoundTripper func(*http.Request) (*http.Response, error)

func (f shutdownRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func shutdownTestSpans() []*logging.Span {
	return []*logging.Span{{Service: "dd-agent-fx-init", Name: "stopSystemProbe", Resource: "system-probe", TraceID: 123, SpanID: 456, Duration: 1234, Meta: map[string]string{"agent.version": "test"}}}
}

func TestShutdownDirectIntakePayload(t *testing.T) {
	cfg := configmock.New(t)
	cfg.Set("api_key", "key-before-rotation", model.SourceAgentRuntime)
	cfg.Set("env", "shutdown-test", model.SourceAgentRuntime)
	cfg.Set("cluster_name", "mathewe-shutdown", model.SourceAgentRuntime)
	cfg.Set("tags", []string{"test:shutdown"}, model.SourceAgentRuntime)
	cfg.Set("apm_config.apm_dd_url", "https://intake.example", model.SourceAgentRuntime)
	sender := newShutdownSender(cfg, "cached-host")
	// A synchronized/rotated key available at shutdown must replace the startup value.
	cfg.Set("api_key", "resolved-current-key", model.SourceAgentRuntime)
	calls := 0
	sender.client.Transport = shutdownRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, "https://intake.example/api/v0.2/traces", req.URL.String())
		assert.Equal(t, "resolved-current-key", req.Header.Get("DD-API-KEY"))
		assert.Equal(t, "application/x-protobuf", req.Header.Get("Content-Type"))
		require.Equal(t, "gzip", req.Header.Get("Content-Encoding"))
		zr, err := gzip.NewReader(req.Body)
		require.NoError(t, err)
		data, err := io.ReadAll(zr)
		require.NoError(t, err)
		require.NoError(t, zr.Close())
		var payload pb.AgentPayload
		require.NoError(t, payload.UnmarshalVT(data))
		assert.Equal(t, "cached-host", payload.HostName)
		assert.Equal(t, "shutdown-test", payload.Env)
		require.Len(t, payload.TracerPayloads, 1)
		require.Len(t, payload.TracerPayloads[0].Chunks, 1)
		chunk := payload.TracerPayloads[0].Chunks[0]
		assert.Equal(t, int32(2), chunk.Priority)
		require.Len(t, chunk.Spans, 1)
		span := chunk.Spans[0]
		assert.Equal(t, uint64(123), span.TraceID)
		assert.Equal(t, "mathewe-shutdown", span.Meta["kube_cluster_name"])
		assert.Equal(t, float64(1), span.Metrics["_dd.analyzed"])
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	require.NoError(t, sender.export(context.Background(), shutdownTestSpans()))
	assert.Equal(t, 1, calls)
	assert.ErrorIs(t, sender.client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

func TestShutdownExportBoundedAfterTraceAgentExit(t *testing.T) {
	cfg := configmock.New(t)
	cfg.Set("api_key", "test-key", model.SourceAgentRuntime)
	synctest.Test(t, func(t *testing.T) {
		sender := newShutdownSender(cfg, "host")
		calls := 0
		sender.client.Transport = shutdownRoundTripper(func(req *http.Request) (*http.Response, error) {
			calls++
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		before := time.Now()
		sender.send(shutdownTestSpans())
		assert.Equal(t, shutdownExportTimeout, time.Since(before))
		assert.Equal(t, 1, calls, "no retry loop may extend process termination")
	})
}

func TestShutdownDestinationPolicyAndCredentials(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		main bool
	}{
		{"no-key", "", true}, {"unresolved-secret", "ENC[private]", true},
		{"unresolved-delegated-auth", "DELA[11111111-1111-1111-1111-111111111111, aws]", true}, {"primary-disabled", "private-key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configmock.New(t)
			cfg.Set("api_key", tc.key, model.SourceAgentRuntime)
			cfg.Set("apm_config.traces_send_to_main_endpoint", tc.main, model.SourceAgentRuntime)
			sender := newShutdownSender(cfg, "host")
			sender.client.Transport = shutdownRoundTripper(func(*http.Request) (*http.Response, error) {
				t.Error("unexpected network request")
				return nil, errors.New("private error")
			})
			err := sender.export(context.Background(), shutdownTestSpans())
			require.Error(t, err)
			if tc.key != "" {
				assert.NotContains(t, err.Error(), tc.key)
			}
		})
	}
}

func TestShutdownDoesNotExposeTransportSecrets(t *testing.T) {
	cfg := configmock.New(t)
	cfg.Set("api_key", "private-api-key", model.SourceAgentRuntime)
	sender := newShutdownSender(cfg, "host")
	sender.client.Transport = shutdownRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://user:password@host?api_key=private-api-key")
	})
	err := sender.export(context.Background(), shutdownTestSpans())
	require.Error(t, err)
	assert.EqualError(t, err, "trace intake request failed")
}

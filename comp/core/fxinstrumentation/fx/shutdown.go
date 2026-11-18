// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package fx

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/DataDog/datadog-agent/pkg/config/model"
	configutils "github.com/DataDog/datadog-agent/pkg/config/utils"
	pb "github.com/DataDog/datadog-agent/pkg/proto/pbgo/trace"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil/logging"
	httputils "github.com/DataDog/datadog-agent/pkg/util/http"
)

const shutdownExportTimeout = 2 * time.Second

// shutdownSender owns its HTTP transport and reads only cached configuration.
// It never needs the local trace-agent, config API, or a running Fx component.
type shutdownSender struct {
	cfg      model.Reader
	hostname string
	client   *http.Client
}

func newShutdownSender(cfg model.Reader, hostname string) *shutdownSender {
	return &shutdownSender{cfg: cfg, hostname: hostname, client: &http.Client{
		Transport: httputils.CreateHTTPTransport(cfg),
		Timeout:   shutdownExportTimeout,
		// API keys must never be forwarded to a redirect destination.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *shutdownSender) send(spans []*logging.Span) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownExportTimeout)
	defer cancel()
	defer s.client.CloseIdleConnections()
	if err := s.export(ctx, spans); err != nil {
		// Do not log errors from transports or endpoints: they can contain credentials.
		fmt.Fprintln(os.Stderr, "[Fx Tracing] Shutdown trace export failed:", err) //nolint:errcheck
	}
}

func (s *shutdownSender) export(ctx context.Context, spans []*logging.Span) error {
	if len(spans) == 0 {
		return nil
	}
	// Diagnostic export must not bypass an explicit prohibition on the primary
	// destination. Additional endpoints and delegated auth need their own exporter.
	if !s.cfg.GetBool("apm_config.traces_send_to_main_endpoint") {
		return errors.New("primary APM destination disabled")
	}
	apiKey := configutils.SanitizeAPIKey(s.cfg.GetString("api_key"))
	if apiKey == "" || strings.HasPrefix(apiKey, "ENC[") || configutils.IsDelaDirective(apiKey) {
		return errors.New("no resolved API key available")
	}
	endpoint := configutils.GetMainEndpoint(s.cfg, "https://trace.agent.", "apm_config.apm_dd_url")
	for _, prefix := range []string{"observability_pipelines_worker", "vector"} {
		if s.cfg.GetBool(prefix + ".traces.enabled") {
			endpoint = s.cfg.GetString(prefix + ".traces.url")
			if endpoint == "" {
				return errors.New("trace pipeline destination missing")
			}
			break
		}
	}
	tags := make(map[string]string)
	for _, tag := range s.cfg.GetStringSlice("tags") {
		k, v, ok := strings.Cut(tag, ":")
		if ok {
			tags[k] = v
		}
	}
	if cluster := s.cfg.GetString("cluster_name"); cluster != "" {
		tags["kube_cluster_name"] = cluster
	}
	env := s.cfg.GetString("env")
	converted := make([]*pb.Span, 0, len(spans))
	for _, span := range spans {
		meta := maps.Clone(tags)
		maps.Copy(meta, span.Meta)
		if env != "" {
			meta["env"] = env
		}
		metrics := map[string]float64{"_dd.analyzed": 1, "_dd1.sr.eausr": 1, "_sampling_priority_v1": 2}
		if span.ParentID == 0 {
			metrics["_top_level"] = 1
		}
		converted = append(converted, &pb.Span{
			Service: span.Service, Name: span.Name, Resource: span.Resource,
			TraceID: span.TraceID, SpanID: span.SpanID, ParentID: span.ParentID,
			Start: span.Start, Duration: span.Duration, Error: span.Error,
			Meta: meta, Metrics: metrics, Type: span.Type,
		})
	}
	payload := &pb.AgentPayload{
		HostName: s.hostname, Env: env, AgentVersion: spans[len(spans)-1].Meta["agent.version"], Tags: tags,
		TracerPayloads: []*pb.TracerPayload{{
			Hostname: s.hostname, Env: env, LanguageName: "go", LanguageVersion: strings.TrimPrefix(runtime.Version(), "go"),
			Chunks: []*pb.TraceChunk{{Priority: 2, Spans: converted}},
		}},
	}
	data, err := payload.MarshalVT()
	if err != nil {
		return errors.New("cannot encode shutdown trace")
	}
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	if _, err := zw.Write(data); err != nil {
		return errors.New("cannot compress shutdown trace")
	}
	if err := zw.Close(); err != nil {
		return errors.New("cannot finish shutdown trace compression")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/api/v0.2/traces", &body)
	if err != nil {
		return errors.New("invalid trace intake destination")
	}
	req.Header.Set("DD-API-KEY", apiKey)
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.New("trace intake request failed")
	}
	defer resp.Body.Close()
	// Bound the response read as well as the request lifetime.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("trace intake returned HTTP %d", resp.StatusCode)
	}
	return nil
}

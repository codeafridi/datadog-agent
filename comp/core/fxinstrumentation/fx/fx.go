// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2025-present Datadog, Inc.

// Package fx provides the fx module for the fxinstrumentation component
package fx

import (
	"context"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/DataDog/datadog-agent/comp/core/config"
	hostnameinterface "github.com/DataDog/datadog-agent/comp/core/hostname/hostnameinterface/def"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil/logging"
	"github.com/DataDog/datadog-agent/pkg/version"
)

type tracingParams struct {
	fx.In
	Config   config.Component
	Hostname hostnameinterface.Component `optional:"true"`
}

// Module enables startup traces via the local trace-agent and shutdown traces
// directly to the configured APM intake, independently of local process order.
// The tracer is implemented in pkg/util/fxutil/logging/tracer.go.
// Traces are sent only when the environment variable DD_FX_TRACING_ENABLED is set to true and this module added to the Fx application.
func Module() fxutil.Module {
	return fxutil.Component(
		// No need to call ProvideComponentConstructor because this module is not a component.
		fx.Invoke(func(logger fxevent.Logger, params tracingParams) {
			if instrumentedLogger, ok := logger.(*logging.FxTracingLogger); ok {
				instrumentedLogger.EnableSpansSending(params.Config.GetString("apm_config.receiver_port"))
				hostname := params.Config.GetString("hostname")
				if params.Hostname != nil {
					hostname = params.Hostname.GetSafe(context.Background())
				}
				sender := newShutdownSender(params.Config, hostname)
				instrumentedLogger.EnableShutdownTracing(version.AgentVersion, sender.send)
			}
		}),
	)
}

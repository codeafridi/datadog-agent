# Core Agent startup traces

Set `DD_FX_TRACING_ENABLED=true` on an Agent with a local trace-agent enabled.
Alongside the existing Fx startup trace, the core Agent sends a diagnostic trace
covering the entire synchronous `startAgent()` call, from function entry until
it returns, including early error returns. No extra configuration is needed in
`agent_telemetry`; this uses the Fx diagnostic transport.

Search APM for:

```text
service:dd-agent-fx-init operation_name:startAgent resource_name:agent
```

The root ends when `startAgent()` returns, even if initialization takes more
than a minute. This differs from Agent Telemetry's `agent.startup` root, which
uses a one-minute timer. The existing Fx `operation_name:startup` root continues
to end at Fx Started, before `startAgent()` executes.

The root and children have `fx.trace_id` linking to the Fx trace,
`process_name` identifying the process, and `agent.version` identifying the
build. Children have operation `startAgent.phase` and fixed resource names:

| Resource | Synchronous work measured |
| --- | --- |
| `startup.log` | Initial version logging. |
| `coredump.Setup` | Core dump setup. |
| `profiling.configure_allocations` | Allocation profiling configuration. |
| `common.SetupInternalProfiling` | Internal profiler setup. |
| `common.GetMainCtxCancel` | Main context retrieval. |
| `procmgr.StartReporter` | Process manager reporter setup. |
| `telemetry.Handler` | Telemetry handler creation and HTTP registration. |
| `hostname.Get` | Hostname lookup. |
| `rcclient.SubscribeAgentTask` | Remote configuration task subscription, when enabled. |
| `datastreams.NewActionsController` | Agent task provider construction, when enabled. |
| `autodiscovery.AddConfigProvider.agent_tasks` | Agent task provider registration, when enabled. |
| `datasecurity.NewController` | Data security provider construction, when enabled. |
| `autodiscovery.AddConfigProvider.data_security` | Data security provider registration, when enabled. |
| `clcrunnerapi.StartCLCRunnerServer` | Cluster check runner API startup, when enabled. |
| `leaderelection.CreateGlobalLeaderEngine` | Leader engine construction, when enabled. |
| `demultiplexer.GetDefaultSender` | Default sender retrieval. |
| `telemetry.RegisterStatsSender` | Stats sender registration, if a sender is available. |
| `installinfo.LogVersionHistory` | Version history update. |
| `check.InitializeInventoryChecksContext` | Inventory checks context initialization. |
| `jmxfetch.InitRunner` | JMX runner initialization. |
| `jmxfetch.RegisterWith` | JMX registration with Autodiscovery. |
| `commonchecks.RegisterChecks` | Core check registration. |
| `collector.InitCheckScheduler` | Check scheduler initialization. |
| `metriclookback.NewSenderManager` | Lookback sender manager creation and scheduler attachment. |
| `autodiscovery.AddScheduler` | Check scheduler registration and existing configuration replay. |
| `demultiplexer.AddAgentStartupTelemetry` | Startup telemetry registration. |
| `autodiscovery.LoadAndRun` | Waiting for background preparation and starting initial providers. |
| `misconfig.ToLog` | Misconfiguration logging. |
| `diagnose.register` | Diagnose callback registration; callbacks are not executed here. |
| `startDependentServicesAsync.launch` | Dispatching dependent service startup; background service startup is not awaited. |

The phase spans measure elapsed call time, including any blocking waits, rather
than CPU time. A call that starts background work measures only dispatch/setup.
In particular, `autodiscovery.LoadAndRun` measures the caller's time waiting for
preparation, not background preparation that already overlapped Fx startup.
The root includes small gaps between phases, including tracing overhead.

An error flag on a phase marks a returned error; a recoverable error such as
core dump setup failure does not mark the root as failed if startup succeeds.
No configuration values, filenames, or error messages are added to these traces.

Traces are opt-in, buffered in memory and sent asynchronously through the
existing Fx transport after completion. A crash, forced exit, unavailable
trace-agent or APM sampling can leave a startup attempt absent. This diagnostic
trace does not measure image pulls, init containers, Fx initialization, first
successful check or first delivered heartbeat, and is not a fleet completeness
metric.

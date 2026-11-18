# Writing an integration test

An integration test runs the components of a pipeline together, in-process, so that it catches the pieces disagreeing with each other, which unit tests on each piece miss. Its value comes from what it keeps real. Each section below starts with the rule, then why it matters.

Worked examples on `main`: `comp/healthplatform/invalidconfig_integration_test.go` and `comp/healthplatform/missedbytes_integration_test.go`. [#60753](https://github.com/DataDog/datadog-agent/pull/60753) adds `comp/healthplatform/invalidconfig_payload_integration_test.go`, which applies every pattern below; read it from that PR until it is merged.

## Placement and naming

Put the test in the package that assembles the pipeline, usually the bundle's root package (`comp/<area>/`) or the package of the last hop, in a `<behavior>_integration_test.go` file with the package's build tags (often `//go:build test`). The name tells readers it is slower than the unit tests around it, and the location gives it access to the bundle's `Bundle()`.

## Wire the real components

Build the graph with `fxutil.Test[T](t, ...)`, using the real `fx/` module (or `Bundle()`) of every hop the behavior flows through. Mock only dependencies outside the behavior's boundary, such as hostname, workloadmeta or telemetry, and be able to say why each one is outside.

Why: a mock in the middle of the pipeline replaces exactly the code whose agreement the test exists to check. When a hop has no real module that runs in a test, say so in the report rather than mocking it silently.

Ask for the components to assert on through an `fx.In` struct:

```go
deps := fxutil.Test[struct {
	fx.In
	Store  storedef.Component
	Schema schema.Component
}](t,
	Bundle(),
	config.Module(),
	fx.Supply(config.NewAgentParams(datadogYAML)),
	schemafx.Module(),
	hostnameinterface.MockModule(), // the hostname is not part of the behavior
	// ...
)
```

## Feed the input the way production does

When the input is configuration, write real files to `t.TempDir()` and load them with the real config components (`config.Module()` with `config.NewAgentParams(path)`, `sysprobeconfigfx.Module()`), not `config.NewMockFromYAML`. The config mocks skip the post-init fixups and allow a dynamic schema, so they don't load configuration like the Agent does.

The config components load into the global configurations. Reset them to the state the Agent starts with, and restore them at the end of the test so tests don't leak into each other:

- If `grep -n "func InitAgentConfigs" pkg/config/mock/*.go` finds it (added by #60753), call `configmock.InitAgentConfigs(t)`. It also sets the mock guards, so `configmock.New` returns the same configurations.
- Otherwise, do what it does in the test: save `pkgconfigsetup.GlobalConfigBuilder()` and `GlobalSystemProbeConfigBuilder()`, call `pkgconfigsetup.InitConfigObjects()`, and restore them with `SetDatadog` and `SetSystemProbe` in `t.Cleanup`. `InitConfigObjects` registers its override functions again on every call, so restore those too, or they pile up across tests. `comp/` may not import `pkg/config/setup` (depguard): prefer adding the helper to `pkg/config/mock` over a lint exception.

Environment variables are read while the configuration is built, so set them first, after clearing the `DD_*` variables of the process: a CI runner's `DD_API_KEY` or `DD_SITE` would otherwise change the result. Use `t.Setenv`, which restores them.

Other inputs follow the same rule: a real socket or listener for network input, a real file for a tailed log, a real check instance for a check.

## Capture the output at the observable boundary

For anything sent to Datadog, point the forwarder at an in-process fakeintake and read it back with its client:

```go
ready := make(chan bool, 1)
fi := fakeintakeserver.NewServer(fakeintakeserver.WithAddress("127.0.0.1:0"), fakeintakeserver.WithReadyChannel(ready))
fi.Start()
require.True(t, <-ready)
t.Cleanup(func() { _ = fi.Stop() })
client := fakeintakeclient.NewClient(fi.URL())
```

Port `0` avoids collisions between tests running in parallel. Shorten the sending interval in the configuration (to around 100ms) so the test waits milliseconds, not minutes.

For status, diagnose or flare output, call the component's provider and assert on what it returns.

## Assert on the whole output, with golden files

When the output is a structured payload, compare all of it against a golden file in `testdata/`, after replacing the volatile values (timestamps, hostname, version, temporary paths, hashed IDs) with placeholders. Keep the scenario inputs next to it, one set of files per scenario, and run one subtest per scenario.

Why: asserting on a few fields lets the others regress silently, which is how a pipeline breaks without any test failing. A golden file also documents the payload for reviewers, and a scenario is added by adding files.

When normalizing, mind the platform: a Windows temporary path appears JSON-escaped in the payload (`C:\\Users\\...`), so replace the escaped form and convert separators, or the test only passes on Linux.

## Wait for events, never for time

Use `require.EventuallyWithT` with a timeout of a few seconds and a short interval, and assert inside it, so the failure message shows the last diff.

Don't assert that something doesn't happen by watching a quiet window (`require.Never` for one second): on a slow runner the output may not have been sent yet, and the test passes without checking anything. Make the absence observable instead: add a canary input known to produce one output, wait for that output, then check it is the only one. For example, an env var set to an invalid value raises one known issue; once it reaches fakeintake, no other configuration issue may be in the report.

When several payloads arrive, pick the latest one explicitly, and break ties in favor of the later arrival.

## Keep tests independent

Restore every global you change (configurations, override functions, registries, environment variables) with `t.Cleanup`. Don't run these tests with `t.Parallel()` when they replace process-wide state. Check that the test also passes when run alone (`bazel test //<package>:<test target> --test_filter=TestX/<case>`), not only after the other tests of the package.

## Say what's not covered

List the boundaries the in-process test doesn't preserve (a real secret backend binary, packaging, the service manager, the real host identity) in the report and the PR description. If one of them matters, keep a single new-e2e scenario for it rather than the whole suite: `write-e2e` can add it.

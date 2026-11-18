---
name: write-test
description: Write the Go test a Datadog Agent change needs, at the right level — a unit test, an integration test wiring the real components with fx and a local fakeintake, or a hand-off to new-e2e. Finds the components the current diff touches, or asks which feature to test and maps the components it involves, then writes, wires and validates the test. Use when asked to "write a test", "add tests for my change", "test this feature", "cover this end to end", "make this an integration test", or "replace this e2e test with something cheaper". Not for running existing tests (run-e2e), judging an existing suite only (e2e-audit), or Python tests.
allowed-tools: Read, Write, Edit, Glob, Grep, Bash, Skill, AskUserQuestion
argument-hint: "[feature or behavior to test — omit to derive it from the current diff]"
---

Produce a test that catches the failures the change could introduce, at the cheapest level that still catches them. The usual mistake this skill prevents is jumping to the extremes: a unit test that mocks away the boundary that breaks, or a new-e2e test that provisions a VM for behavior an in-process integration test covers in seconds.

**Owning team:** `@DataDog/agent-devx`

**Prerequisites:** run from the repository root, with `dda` and Bazel set up (`dda inv install-tools`) and `origin/main` fetched. Go tests only: Python checks and invoke tasks are out of scope.

## References

| File | Load when |
|---|---|
| `references/integration.md` | Step 3 picks an integration test — before writing it. It holds the wiring patterns and the pitfalls that make such tests weak or flaky. |
| `doc/guidelines/testing/test-categories.md` | You need the repository's definition of a test category to explain a verdict. |

## Step 0 — Establish the scope

With an argument, treat it as the feature to test. Otherwise derive the scope from the change:

```bash
git status --short
git diff --stat $(git merge-base HEAD origin/main)
```

When the diff has no Go change, or the user wants to test something other than the diff, ask which feature or behavior to test with `AskUserQuestion`, offering the candidates you can see (recent commits, the files they mention). A behavior is something a user, another component, or Datadog can observe: a payload sent to intake, a status or diagnose output, a config key's effect, a CLI result. Restate it in that form before going on, because the rest of the procedure maps and tests *that* behavior, not the files.

## Step 1 — Map the components involved

List the components the behavior flows through, from where its input enters to where its output is observed. These are the candidates to wire for real in a test.

1. **Find the entry points.**
   - From the diff: take the changed Go files.
   - From a feature: find the code that implements it. Search for what a user would name: the config key (use the `locate-config-setting` skill for its schema node), the metric, tag, issue, or log message, the CLI subcommand, the intake endpoint. `git grep -n` the literal and keep the non-test hits.
2. **Map them to components:**
   ```bash
   python3 -I .agents/skills/write-test/scripts/map_components.py [<path>...]
   ```
   Without arguments it maps the Go files changed against `origin/main`. For each component or bundle reached, it prints the team, the directories that can build it in a test (`fx/`, `impl/`, `fx-mock/`, `mock/`, `fx-noop/`, `noop-impl/`, or `Bundle()`), and its consumers. A `pkg/` package is mapped to the components importing it; when none imports it directly, pass its importers to the script to follow the chain.
3. **Follow the pipeline.** Read the `Requires` struct of each changed component's implementation (`impl/*.go`) for its upstream dependencies, and the consumers column for downstream ones. Stop at the boundary where the behavior becomes observable: a forwarder sending to intake, a status provider, a diagnose function, a file written. Read the code along the way rather than assuming the data path from names.
4. **Report the pipeline** before choosing a level, as a short chain with each hop's component and what it contributes, for example:

   > `datadog.yaml` + `DD_*` env → `comp/core/config` (loads, validates with `comp/core/schema`) → `comp/healthplatform` invalid-config check → store → forwarder → intake `/api/v2/agenthealth`

When the user named a feature and the mapping is ambiguous (several implementations, or a feature split across binaries), ask which path matters rather than testing all of them.

## Step 2 — Choose the level

Invoke the `e2e-audit` skill with the behavior and the pipeline from Step 1. It decides between a unit test, an integration test and new-e2e, by asking which boundary each assertion needs to keep real. Then:

- **E2E justified:** invoke `write-e2e` with the behavior, prefixed with `e2e-audit verdict: E2E justified — <reason>` and the hops it named as needing the deployed Agent, then stop. The prefix lets `write-e2e` skip its own audit, so the behavior is audited once and the two skills can't send it back and forth.
- **Integration test:** load `references/integration.md` and continue.
- **Unit test:** continue.

A behavior often needs both: unit tests for the logic of the changed component, and one integration test proving the pipeline still carries it end to end. That pipeline test is the one most changes are missing — unit tests on each piece don't catch two pieces disagreeing (a schema rejecting what the loader produces, a payload field the forwarder drops). Propose it whenever the pipeline spans more than one component, even if the user asked for a unit test.

## Step 3 — Write the test

Read the existing tests next to the code first and follow their conventions: helpers, mocks, table layout, and build tags. The repository's test conventions win over this skill.

**Unit test**, in the package of the logic under test (`impl/` for a component):

- Table-driven, one case per behavior, named after the behavior.
- Mock only what the logic doesn't own, with the component's own `mock/` or `fx-mock/` package, or `config.NewMockWithOverrides` for settings.
- Assert on outputs and errors, not on calls to mocks, unless the call is the behavior.

**Integration test:** follow `references/integration.md`. In short: build the pipeline with the real `fx/` modules of every hop from Step 1, feed the input the way production does, capture the output at the observable boundary, and assert on the whole output against golden files.

For both, match the header and build tags of the package's other test files, and keep each test independent: no order dependency, no global state left behind.

## Step 4 — Wire it into the build

```bash
bazel run //:gazelle -- <package dir>
```

Run it after adding a test file or `testdata/` files, and commit the `BUILD.bazel` diff: CI fails otherwise (`bazel:run-gazelle`). Check that files the test reads at runtime are in the test target's `data` (gazelle adds `glob(["testdata/**"])`) and that `go:embed` files are in `embedsrcs`. Gazelle doesn't always remove entries for deleted files; remove those by hand.

## Step 5 — Validate

1. Run it: `dda inv test --targets=./<package>`. Read the subtest results in the Bazel test log, not only the target status: a test that skips (for example, when a generated file is missing) passes too.
2. Prove it fails: break the behavior once (an input fixture, a condition in the code), check that the test fails with a message naming the problem, then revert the break and check `git diff` holds only the test change. A test that still passes was asserting on something else.
3. Lint: `dda inv linter.go --targets=./<package>`. Typecheck other platforms when the test has platform-specific code (see the root `AGENTS.md`).

## Output

Report:

```markdown
**Behavior:** <the behavior, as observable>
**Pipeline:** <the chain from Step 1>
**Level:** <unit | integration | new-e2e> — <the e2e-audit reason, one line>

| Test | Package | Real | Mocked, and why |
| --- | --- | --- | --- |
| `TestX/<case>` | `comp/...` | <components wired for real> | <dependency>: <outside the boundary because…> |

**Validated:** <test command result> · <the deliberate break, the failure it produced, and that it was reverted> · <lint result>
**Not covered:** <boundaries this test doesn't preserve, if any, and whether another level covers them>
```

The skill succeeded when the test fails on the reverted deliberate break and passes otherwise, lint is clean, and every mock in the table has a reason that places it outside the behavior's boundary. For an integration test, the output is also asserted as a whole (a golden file, or every field of the payload); for a unit test, every case of the table asserts on an output or an error.

## Example

**Input:** "Test that invalid settings in `datadog.yaml` reach intake as health issues, without leaking their values."

**Pipeline:** `datadog.yaml`, `system-probe.yaml` and `DD_*` env → `comp/core/config` and `comp/core/sysprobeconfig` (load, validate with `comp/core/schema`) → `comp/healthplatform` checks → store → forwarder → intake.

**Level:** integration — every hop runs in-process, and intake is replaced by a local fakeintake without losing the payload boundary. The first version of this test was a new-e2e suite provisioning an EC2 host per run; it became `comp/healthplatform/invalidconfig_payload_integration_test.go` ([#60753](https://github.com/DataDog/datadog-agent/pull/60753)): the same 9 scenarios and golden reports, in about 4 seconds, failing on the same regressions (in review when this skill was written). What it no longer covers — a real secret backend binary and the systemd environment file — is listed under "Not covered".

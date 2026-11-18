#!/usr/bin/env python3
# Unless explicitly stated otherwise all files in this repository are licensed
# under the Apache License Version 2.0.
# This product includes software developed at Datadog (https://www.datadoghq.com/).
# Copyright 2026-present Datadog, Inc.
"""Map Go source paths to the Agent components they belong to, and list how to wire each component in a test.

Usage, from the repository root:
    map_components.py                # the Go files changed against the merge base with origin/main
    map_components.py <path>...      # files or directories, e.g. the ones a feature touches

For every component or bundle reached, it prints its team, the fx modules and mocks available to wire it in a test,
and the packages that import it (its consumers, the next hops of the pipeline). A pkg/ path is mapped to the
components importing it. The output is Markdown, meant to be read by the write-test skill.
"""

import os
import re
import subprocess
import sys
from collections import defaultdict

MODULE = "github.com/DataDog/datadog-agent"
# directories that hold a way to build the component in a test, from the most to the least real
WIRING_DIRS = ["fx", "impl", "fx-mock", "mock", "fx-noop", "noop-impl"]
MAX_CONSUMERS = 8


def git(*args):
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout


def changed_go_files():
    base = git("merge-base", "HEAD", "origin/main").strip()
    tracked = git("diff", "--name-only", base).splitlines()
    untracked = git("ls-files", "--others", "--exclude-standard").splitlines()
    return sorted({p for p in tracked + untracked if p.endswith(".go") and os.path.exists(p)})


def expand(paths):
    files = []
    for path in paths:
        if os.path.isdir(path):
            files += [p for p in git("ls-files", "--", path).splitlines() if p.endswith(".go")]
        else:
            files.append(path)
    return files


def component_root(path):
    """Return (root, kind) of the component or bundle containing path, or (None, None) outside comp/."""
    directory = os.path.dirname(path)
    while directory.startswith("comp/") or directory == "comp":
        if os.path.exists(os.path.join(directory, "def", "component.go")):
            return directory, "component"
        if os.path.exists(os.path.join(directory, "component.go")):
            return directory, "component (legacy layout)"
        if os.path.exists(os.path.join(directory, "bundle.go")):
            return directory, "bundle"
        if os.path.basename(directory) in WIRING_DIRS + ["def"]:
            directory = os.path.dirname(directory)
            continue
        directory = os.path.dirname(directory)
    return None, None


def team(root, kind):
    candidates = [
        os.path.join(root, "def", "component.go"),
        os.path.join(root, "component.go"),
        os.path.join(root, "bundle.go"),
    ]
    for candidate in candidates:
        if os.path.exists(candidate):
            with open(candidate, encoding="utf-8") as f:
                match = re.search(r"^// team: (.+)$", f.read(), re.MULTILINE)
            if match:
                return match.group(1).strip()
    return "?"


def importers(package_dir):
    """Return the directories of the non-test Go files importing package_dir."""
    pattern = f'"{MODULE}/{package_dir}"'
    try:
        output = git("grep", "-l", "--fixed-strings", pattern, "--", "*.go", ":!*_test.go")
    except subprocess.CalledProcessError:  # no match
        return set()
    return {os.path.dirname(p) for p in output.splitlines()}


def describe(directory):
    """Name a consumer directory by its component or bundle, or by the directory itself."""
    root, _ = component_root(os.path.join(directory, "x.go"))
    return root or directory


def main():
    files = expand(sys.argv[1:]) if len(sys.argv) > 1 else changed_go_files()
    if not files:
        print("No Go file to map.")
        return

    components = defaultdict(set)  # root -> changed files
    kinds = {}
    via_pkg = defaultdict(set)  # root -> pkg/ packages it imports
    unmapped = []
    for path in files:
        root, kind = component_root(path)
        if root:
            components[root].add(path)
            kinds[root] = kind
            continue
        package_dir = os.path.dirname(path)
        reached = {component_root(os.path.join(d, "x.go")) for d in importers(package_dir)}
        reached = {r for r in reached if r[0]}
        if not reached:
            unmapped.append(path)
        for root, kind in reached:
            via_pkg[root].add(package_dir)
            kinds[root] = kind

    print("| Component | Kind | Team | Reached by | Test wiring | Consumers |")
    print("| --- | --- | --- | --- | --- | --- |")
    for root in sorted(set(components) | set(via_pkg)):
        reached_by = sorted(f"`{os.path.relpath(p, root)}`" for p in components.get(root, ()))
        reached_by += sorted(f"imports `{p}`" for p in via_pkg.get(root, ()))
        wiring = [f"`{d}/`" for d in WIRING_DIRS if os.path.isdir(os.path.join(root, d))]
        if kinds[root] == "bundle":
            wiring.insert(0, "`Bundle()`")
        def_dir = os.path.join(root, "def") if os.path.isdir(os.path.join(root, "def")) else root
        consumers = sorted({describe(d) for d in importers(def_dir)} - {root})
        shown = ", ".join(f"`{c}`" for c in consumers[:MAX_CONSUMERS])
        if len(consumers) > MAX_CONSUMERS:
            shown += f", +{len(consumers) - MAX_CONSUMERS} more"
        print(
            f"| `{root}` | {kinds[root]} | {team(root, kinds[root])} | {', '.join(reached_by)} "
            f"| {', '.join(wiring) or '-'} | {shown or '-'} |"
        )
    if unmapped:
        print(
            "\nNo component imports these packages directly: pass their importers (`git grep -l '\"<import path>\"'`) to"
        )
        print("this script to follow the pipeline up to a component, or test them as plain packages:")
        for path in unmapped:
            print(f"- `{path}`")


if __name__ == "__main__":
    main()

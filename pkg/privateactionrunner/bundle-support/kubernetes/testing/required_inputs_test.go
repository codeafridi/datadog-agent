// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2026-present Datadog, Inc.

package testing

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	testhelpers "github.com/DataDog/datadog-agent/pkg/privateactionrunner/bundle-support/test"
	"github.com/DataDog/datadog-agent/pkg/privateactionrunner/types"
)

func generatedActionTask(inputs map[string]interface{}) *types.Task {
	return testhelpers.NewTestTask("test", "test", &types.Attributes{Inputs: inputs})
}

func assertGeneratedActionsRejectMissingInputs(t *testing.T, bundle types.Bundle, resources []string) {
	for _, resource := range resources {
		for _, verb := range []string{"get", "delete", "patch"} {
			for _, inputs := range []map[string]interface{}{nil, {}, {"namespace": "test"}, {"name": ""}, {"dryRun": "All"}} {
				t.Run(verb+resource, func(t *testing.T) {
					_, err := bundle.GetAction(verb+resource).Run(context.Background(), generatedActionTask(inputs), nil)
					require.EqualError(t, err, "name is required")
				})
			}
		}
		for _, verb := range []string{"create", "update", "patch"} {
			t.Run(verb+resource+"/missing body", func(t *testing.T) {
				_, err := bundle.GetAction(verb+resource).Run(context.Background(), generatedActionTask(map[string]interface{}{"namespace": "test", "name": "test"}), nil)
				require.EqualError(t, err, "body is required")
			})
		}
		if !strings.HasPrefix(resource, "Cluster") {
			t.Run(resource+"/missing namespace", func(t *testing.T) {
				_, err := bundle.GetAction("get"+resource).Run(context.Background(), generatedActionTask(map[string]interface{}{"name": "test"}), nil)
				require.EqualError(t, err, "namespace is required")
			})
		}
	}
}

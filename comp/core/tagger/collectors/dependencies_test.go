// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package collectors

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/DataDog/datadog-agent/comp/core/config"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	logmock "github.com/DataDog/datadog-agent/comp/core/log/mock"
	"github.com/DataDog/datadog-agent/comp/core/tagger/common"
	"github.com/DataDog/datadog-agent/comp/core/tagger/types"
	workloadmeta "github.com/DataDog/datadog-agent/comp/core/workloadmeta/def"
	workloadmetafxmock "github.com/DataDog/datadog-agent/comp/core/workloadmeta/fx-mock"
	workloadmetamock "github.com/DataDog/datadog-agent/comp/core/workloadmeta/mock"
	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes"
)

func TestDependencyIndex(t *testing.T) {
	pod := &workloadmeta.KubernetesPod{EntityID: workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesPod, ID: "pod"}}
	workload := entityDependency(workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesKueueWorkload, ID: "default/job"})
	flavor := entityDependency(workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesKueueResourceFlavor, ID: "a100"})

	d := newDependencyIndex()

	complete := workloadmeta.Event{Type: workloadmeta.EventTypeSet, Entity: pod, IsComplete: true}
	d.replace(complete, map[dependency]struct{}{workload: {}, flavor: {}})
	assert.ElementsMatch(t, []workloadmeta.EntityID{pod.EntityID}, d.readersOf(workload))
	assert.ElementsMatch(t, []workloadmeta.EntityID{pod.EntityID}, d.readersOf(flavor))
	ev, ok := d.lastEvent(pod.EntityID)
	assert.True(t, ok)
	assert.Equal(t, complete, ev)

	incomplete := workloadmeta.Event{Type: workloadmeta.EventTypeSet, Entity: pod}
	d.replace(incomplete, map[dependency]struct{}{flavor: {}})
	assert.Empty(t, d.readersOf(workload))
	assert.ElementsMatch(t, []workloadmeta.EntityID{pod.EntityID}, d.readersOf(flavor))
	ev, _ = d.lastEvent(pod.EntityID)
	assert.Equal(t, incomplete, ev)

	d.forget(pod.EntityID)
	assert.Empty(t, d.readersOf(flavor))
	assert.Empty(t, d.readers)
	assert.Empty(t, d.tagged)
	_, ok = d.lastEvent(pod.EntityID)
	assert.False(t, ok)
}

func TestDependencyIndexChangedIncludesPreviousKeys(t *testing.T) {
	autoscaler := &workloadmeta.KubernetesAutoscaler{EntityID: workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesAutoscaler, ID: "default/my-hpa"}}
	autoscalerID := entityDependency(autoscaler.EntityID)
	target := func(key string) dependency { return keyDependency(workloadmeta.KindKubernetesAutoscaler, key) }
	d := newDependencyIndex()

	set := workloadmeta.Event{Type: workloadmeta.EventTypeSet, Entity: autoscaler}
	assert.ElementsMatch(t, []dependency{autoscalerID, target("app-a")}, d.changed(set, []string{"app-a"}))

	// The autoscaler targets another workload: readers of both are affected.
	assert.ElementsMatch(t, []dependency{autoscalerID, target("app-a"), target("app-b")}, d.changed(set, []string{"app-b"}))

	// The entity lost its key: readers of the old one are still affected.
	assert.ElementsMatch(t, []dependency{autoscalerID, target("app-b")}, d.changed(set, nil))
	assert.Empty(t, d.keys)

	d.changed(set, []string{"app-a"})
	unset := workloadmeta.Event{Type: workloadmeta.EventTypeUnset, Entity: autoscaler}
	assert.ElementsMatch(t, []dependency{autoscalerID, target("app-a")}, d.changed(unset, nil))
	assert.Empty(t, d.keys)
}

func TestTrackingStoreRecordsLookupsOnlyWhileTracking(t *testing.T) {
	s := &trackingStore{Component: newDependencyTestStore(t)}

	_, _ = s.GetKubernetesKueueWorkload("default/untracked")

	s.startTracking()
	// The entities are not in the store. Lookups that find nothing are
	// recorded too, so that the entity arriving later re-tags the reader.
	_, _ = s.GetKubernetesKueueWorkload("default/job")
	_, _ = s.GetKubernetesKueueResourceFlavor("a100")
	reads := s.stopTracking()

	assert.Equal(t, map[dependency]struct{}{
		entityDependency(workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesKueueWorkload, ID: "default/job"}): {},
		entityDependency(workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesKueueResourceFlavor, ID: "a100"}):  {},
	}, reads)

	_, _ = s.GetKubernetesKueueWorkload("default/untracked")
	assert.Nil(t, s.stopTracking())
}

func TestKueueWorkloadArrivingAfterPodRetagsPod(t *testing.T) {
	h := newDependencyTestHarness(t)

	got := h.add(newContainer("ctr-a"), newKueuePod("pod-a", "ctr-a", "job-a"))
	podTags := tagInfoFor(got, podTaggerID("pod-a"))
	require.NotNil(t, podTags)
	assert.NotContains(t, podTags.OrchestratorCardTags, "kueue_workload:job-a")

	got = h.add(newKueueWorkload("job-a", "a100"))

	for _, id := range []types.EntityID{podTaggerID("pod-a"), containerTaggerID("ctr-a")} {
		tagInfo := tagInfoFor(got, id)
		require.NotNil(t, tagInfo, "%s was not re-tagged", id)
		assert.Contains(t, tagInfo.OrchestratorCardTags, "kueue_workload:job-a", "tags for %s", id)
		assert.Contains(t, tagInfo.LowCardTags, "kueue_resource_flavor:a100", "tags for %s", id)
		assert.True(t, tagInfo.IsComplete, "completeness for %s", id)
	}
}

func TestKueueWorkloadRemovalRetagsPod(t *testing.T) {
	h := newDependencyTestHarness(t)
	workload := newKueueWorkload("job-a", "a100")

	got := h.add(newContainer("ctr-a"), newKueuePod("pod-a", "ctr-a", "job-a"), workload)
	require.Contains(t, tagInfoFor(got, podTaggerID("pod-a")).OrchestratorCardTags, "kueue_workload:job-a")

	h.store.Unset(workload)
	got = h.process(unsetEvent(workload))

	podTags := tagInfoFor(got, podTaggerID("pod-a"))
	require.NotNil(t, podTags, "pod was not re-tagged")
	assert.NotContains(t, podTags.OrchestratorCardTags, "kueue_workload:job-a")
}

func TestKueueResourceFlavorChangeRetagsOnlyPodsUsingIt(t *testing.T) {
	h := newDependencyTestHarness(t)

	got := h.add(
		newContainer("ctr-a"),
		newContainer("ctr-b"),
		newKueuePod("pod-a", "ctr-a", "job-a"),
		newKueuePod("pod-b", "ctr-b", "job-b"),
		newKueueWorkload("job-a", "a100"),
		newKueueWorkload("job-b", "h100"),
		newKueueResourceFlavor("a100", "ampere"),
		newKueueResourceFlavor("h100", "hopper"),
	)
	require.Contains(t, tagInfoFor(got, podTaggerID("pod-a")).LowCardTags, "gpu_architecture:ampere")

	got = h.add(newKueueResourceFlavor("a100", "blackwell"))

	podATags := tagInfoFor(got, podTaggerID("pod-a"))
	require.NotNil(t, podATags, "pod-a was not re-tagged")
	assert.Contains(t, podATags.LowCardTags, "gpu_architecture:blackwell")
	assert.NotNil(t, tagInfoFor(got, kueueWorkloadTaggerID("job-a")), "job-a was not re-tagged")

	assert.Nil(t, tagInfoFor(got, podTaggerID("pod-b")), "pod-b does not use the flavor")
	assert.Nil(t, tagInfoFor(got, kueueWorkloadTaggerID("job-b")), "job-b does not use the flavor")
}

func TestDeletedPodIsNotRetagged(t *testing.T) {
	h := newDependencyTestHarness(t)
	pod := newKueuePod("pod-a", "ctr-a", "job-a")
	workload := newKueueWorkload("job-a", "a100")
	h.add(newContainer("ctr-a"), pod, workload)

	h.store.Unset(pod)
	h.process(unsetEvent(pod))
	assert.Empty(t, h.collector.dependencies.readersOf(entityDependency(workload.EntityID)))

	got := h.add(newKueueWorkload("job-a", "h100"))
	assert.Nil(t, tagInfoFor(got, podTaggerID("pod-a")))
}

func TestEntityInBundleIsTaggedOnce(t *testing.T) {
	h := newDependencyTestHarness(t)
	pod := newKueuePod("pod-a", "ctr-a", "job-a")
	workload := newKueueWorkload("job-a", "a100")
	h.add(newContainer("ctr-a"), pod, workload)

	got := h.process(setEvent(pod), setEvent(workload))

	count := 0
	for _, tagInfo := range got {
		if tagInfo.EntityID == podTaggerID("pod-a") {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestProcessLosingPromotableTagRetagsContainer(t *testing.T) {
	h := newDependencyTestHarness(t)
	h.collector.promoteProcessTags = true

	h.add(newContainer("ctr-a"))
	got := h.add(newProcessWithTags(1, "ctr-a", "training_run_id:run-1"))
	assert.Contains(t, tagInfoFor(got, containerTaggerID("ctr-a")).LowCardTags, "training_run_id:run-1")

	// The process no longer has the tag. Its new state has no key, but the
	// container read the key the process had.
	got = h.add(newProcessWithTags(1, "ctr-a", "foo:bar"))
	containerTags := tagInfoFor(got, containerTaggerID("ctr-a"))
	require.NotNil(t, containerTags, "ctr-a was not re-tagged")
	assert.NotContains(t, containerTags.LowCardTags, "training_run_id:run-1")
}

// When one source of a pod is removed while another remains, workloadmeta
// sends a set event with the pod as it was before the removal, so that its
// tags don't change until the last source is removed. A dependency change
// must not re-tag the pod from the store, which already lost the removed
// source's data, neither in the same bundle nor in a later one.
func TestPartialUnsetKeepsReaderStateOnDependencyChange(t *testing.T) {
	for _, sameBundle := range []bool{true, false} {
		t.Run(fmt.Sprintf("same bundle %t", sameBundle), func(t *testing.T) {
			h := newDependencyTestHarness(t)
			podID := workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesPod, ID: "pod-a"}
			// The PodGroup annotation only comes from the kubelet.
			fromKubelet := &workloadmeta.KubernetesPod{
				EntityID: podID,
				EntityMeta: workloadmeta.EntityMeta{
					Name:        "pod-a",
					Namespace:   "default",
					Annotations: map[string]string{kubernetes.KAIPodGroupAnnotationKey: "pg"},
				},
			}
			fromCluster := &workloadmeta.KubernetesPod{
				EntityID:   podID,
				EntityMeta: workloadmeta.EntityMeta{Name: "pod-a", Namespace: "default"},
			}
			h.store.Notify([]workloadmeta.CollectorEvent{
				{Type: workloadmeta.EventTypeSet, Source: workloadmeta.SourceNodeOrchestrator, Entity: fromKubelet},
				{Type: workloadmeta.EventTypeSet, Source: workloadmeta.SourceClusterOrchestrator, Entity: fromCluster},
			})
			beforeRemoval, err := h.store.GetKubernetesPod(podID.ID)
			require.NoError(t, err)

			got := h.add(newKAIPodGroup("pg", "queue-a"))
			got = append(got, h.process(setEvent(beforeRemoval))...)
			require.Contains(t, kaiTags(tagInfoFor(got, podTaggerID("pod-a"))), "kai_queue:queue-a")

			// The kubelet removes the pod first. The tagger gets a set event
			// for the pod as it was before the removal.
			h.store.Notify([]workloadmeta.CollectorEvent{
				{Type: workloadmeta.EventTypeUnset, Source: workloadmeta.SourceNodeOrchestrator, Entity: fromKubelet},
			})
			afterRemoval, err := h.store.GetKubernetesPod(podID.ID)
			require.NoError(t, err)
			require.Empty(t, afterRemoval.Annotations, "the store no longer has the kubelet's annotation")

			updated := newKAIPodGroup("pg", "queue-b")
			h.store.Set(updated)
			if sameBundle {
				got = h.process(setEvent(beforeRemoval), setEvent(updated))
			} else {
				h.process(setEvent(beforeRemoval))
				got = h.process(setEvent(updated))
			}

			podTags := tagInfoFor(got, podTaggerID("pod-a"))
			require.NotNil(t, podTags, "pod-a was not re-tagged")
			assert.Contains(t, kaiTags(podTags), "kai_queue:queue-b")
			assert.Contains(t, kaiTags(podTags), "kai_pod_group:pg")
		})
	}
}

func TestPodTaggedBeforeItsAutoscalerInTheSameBundle(t *testing.T) {
	h := newDependencyTestHarness(t)
	pod := newTestAutoscaledPod("pod-a", "my-ns", "app-a")
	hpa := newTestAutoscaler(workloadmeta.AutoscalerKindHPA, "my-ns", "my-hpa", workloadmeta.AutoscalerTargetDeployment, "app-a")
	h.store.Set(pod)

	// The pod is tagged first, and isn't re-tagged later in the bundle, so it
	// must already see the autoscaler.
	got := h.process(setEvent(pod), setEvent(hpa))

	podTags := tagInfoFor(got, podTaggerID("pod-a"))
	require.NotNil(t, podTags)
	assert.Contains(t, podTags.LowCardTags, "kube_autoscaler_kind:horizontalpodautoscaler")
}

// The autoscaler re-tag must use the same pod snapshot as the dependency
// re-tag, and not re-tag the pod a second time from the store.
func TestAutoscalerRetagUsesReaderSnapshot(t *testing.T) {
	h := newDependencyTestHarness(t)
	podID := workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesPod, ID: "pod-a"}
	owners := []workloadmeta.KubernetesPodOwner{{Kind: kubernetes.DeploymentKind, Name: "app-a"}}
	withAnnotation := &workloadmeta.KubernetesPod{
		EntityID: podID,
		EntityMeta: workloadmeta.EntityMeta{
			Name:        "pod-a",
			Namespace:   "default",
			Annotations: map[string]string{kubernetes.KAIPodGroupAnnotationKey: "pg"},
		},
		Owners: owners,
	}
	withoutAnnotation := &workloadmeta.KubernetesPod{
		EntityID:   podID,
		EntityMeta: workloadmeta.EntityMeta{Name: "pod-a", Namespace: "default"},
		Owners:     owners,
	}
	h.store.Notify([]workloadmeta.CollectorEvent{
		{Type: workloadmeta.EventTypeSet, Source: workloadmeta.SourceNodeOrchestrator, Entity: withAnnotation},
		{Type: workloadmeta.EventTypeSet, Source: workloadmeta.SourceClusterOrchestrator, Entity: withoutAnnotation},
	})
	beforeRemoval, err := h.store.GetKubernetesPod(podID.ID)
	require.NoError(t, err)
	h.add(newKAIPodGroup("pg", "queue-a"))
	h.process(setEvent(beforeRemoval))

	// The source with the annotation is removed; the tagger gets the pod as it
	// was before the removal.
	h.store.Notify([]workloadmeta.CollectorEvent{
		{Type: workloadmeta.EventTypeUnset, Source: workloadmeta.SourceNodeOrchestrator, Entity: withAnnotation},
	})
	h.process(setEvent(beforeRemoval))

	updated := newKAIPodGroup("pg", "queue-b")
	h.store.Set(updated)
	hpa := newTestAutoscaler(workloadmeta.AutoscalerKindHPA, "default", "my-hpa", workloadmeta.AutoscalerTargetDeployment, "app-a")
	got := h.process(setEvent(updated), setEvent(hpa))

	count := 0
	for _, tagInfo := range got {
		if tagInfo.EntityID == podTaggerID("pod-a") {
			count++
		}
	}
	assert.Equal(t, 1, count, "the pod is re-tagged once")

	podTags := tagInfoFor(got, podTaggerID("pod-a"))
	require.NotNil(t, podTags)
	assert.Contains(t, kaiTags(podTags), "kai_queue:queue-b")
	assert.Contains(t, podTags.LowCardTags, "kube_autoscaler_kind:horizontalpodautoscaler")
}

// After a partial unset the store's pod can lack the owner that the pod's
// current tags were computed from. Removing its autoscaler must still re-tag
// it, including when the pod reads nothing else, like Kueue or KAI entities.
func TestAutoscalerRemovalRetagsPodAfterPartialUnset(t *testing.T) {
	h := newDependencyTestHarness(t)
	podID := workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesPod, ID: "pod-a"}
	// The owner only comes from the kubelet.
	fromKubelet := &workloadmeta.KubernetesPod{
		EntityID:   podID,
		EntityMeta: workloadmeta.EntityMeta{Name: "pod-a", Namespace: "default"},
		Owners:     []workloadmeta.KubernetesPodOwner{{Kind: kubernetes.DeploymentKind, Name: "app-a"}},
	}
	fromCluster := &workloadmeta.KubernetesPod{
		EntityID:   podID,
		EntityMeta: workloadmeta.EntityMeta{Name: "pod-a", Namespace: "default"},
	}
	hpa := newTestAutoscaler(workloadmeta.AutoscalerKindHPA, "default", "my-hpa", workloadmeta.AutoscalerTargetDeployment, "app-a")
	h.store.Notify([]workloadmeta.CollectorEvent{
		{Type: workloadmeta.EventTypeSet, Source: workloadmeta.SourceNodeOrchestrator, Entity: fromKubelet},
		{Type: workloadmeta.EventTypeSet, Source: workloadmeta.SourceClusterOrchestrator, Entity: fromCluster},
	})
	beforeRemoval, err := h.store.GetKubernetesPod(podID.ID)
	require.NoError(t, err)
	got := h.process(setEvent(beforeRemoval), setEvent(hpa))
	require.Contains(t, tagInfoFor(got, podTaggerID("pod-a")).LowCardTags, "kube_autoscaler_kind:horizontalpodautoscaler")

	// The kubelet removes the pod first: the store's pod loses its owner, and
	// the tagger gets the pod as it was before the removal.
	h.store.Notify([]workloadmeta.CollectorEvent{
		{Type: workloadmeta.EventTypeUnset, Source: workloadmeta.SourceNodeOrchestrator, Entity: fromKubelet},
	})
	h.process(setEvent(beforeRemoval))

	got = h.process(unsetEvent(hpa))
	podTags := tagInfoFor(got, podTaggerID("pod-a"))
	require.NotNil(t, podTags, "pod-a was not re-tagged after its autoscaler was removed")
	assert.NotContains(t, podTags.LowCardTags, "kube_autoscaler_kind:horizontalpodautoscaler")
}

type dependencyTestHarness struct {
	store     workloadmetamock.Mock
	collector *WorkloadMetaCollector
}

func newDependencyTestHarness(t *testing.T) *dependencyTestHarness {
	store := newDependencyTestStore(t)
	return &dependencyTestHarness{
		store:     store,
		collector: NewWorkloadMetaCollector(context.Background(), configmock.New(t), store, nil),
	}
}

func newDependencyTestStore(t *testing.T) workloadmetamock.Mock {
	return fxutil.Test[workloadmetamock.Mock](t, fx.Options(
		fx.Provide(func() log.Component { return logmock.New(t) }),
		fx.Provide(func() config.Component { return config.NewMock(t) }),
		fx.Supply(context.Background()),
		workloadmetafxmock.MockModule(workloadmeta.NewParams()),
	))
}

// add sets the entities in the store and processes their set events as one
// bundle, as workloadmeta would for a single Notify.
func (h *dependencyTestHarness) add(entities ...workloadmeta.Entity) []*types.TagInfo {
	events := make([]workloadmeta.Event, 0, len(entities))
	for _, entity := range entities {
		h.store.Set(entity)
		events = append(events, setEvent(entity))
	}
	return h.process(events...)
}

// process runs one bundle through the collector and returns the tags it
// produced.
func (h *dependencyTestHarness) process(events ...workloadmeta.Event) []*types.TagInfo {
	var got []*types.TagInfo
	h.collector.tagProcessor = processorFunc(func(tagInfos []*types.TagInfo) {
		got = append(got, tagInfos...)
	})
	h.collector.processEvents(workloadmeta.EventBundle{Events: events, Ch: make(chan struct{})})
	return got
}

type processorFunc func([]*types.TagInfo)

func (f processorFunc) ProcessTagInfo(tagInfos []*types.TagInfo) {
	f(tagInfos)
}

func tagInfoFor(tagInfos []*types.TagInfo, id types.EntityID) *types.TagInfo {
	var found *types.TagInfo
	for _, tagInfo := range tagInfos {
		if tagInfo.EntityID == id && !tagInfo.DeleteEntity {
			found = tagInfo
		}
	}
	return found
}

func setEvent(entity workloadmeta.Entity) workloadmeta.Event {
	return workloadmeta.Event{Type: workloadmeta.EventTypeSet, Entity: entity, IsComplete: true}
}

func unsetEvent(entity workloadmeta.Entity) workloadmeta.Event {
	return workloadmeta.Event{Type: workloadmeta.EventTypeUnset, Entity: entity, IsComplete: true}
}

func podTaggerID(uid string) types.EntityID {
	return types.NewEntityID(types.KubernetesPodUID, uid)
}

func containerTaggerID(id string) types.EntityID {
	return types.NewEntityID(types.ContainerID, id)
}

func kueueWorkloadTaggerID(name string) types.EntityID {
	return common.BuildTaggerEntityID(workloadmeta.EntityID{
		Kind: workloadmeta.KindKubernetesKueueWorkload,
		ID:   workloadmeta.GenerateKueueWorkloadEntityID("default", name),
	})
}

func newContainer(id string) *workloadmeta.Container {
	return &workloadmeta.Container{
		EntityID: workloadmeta.EntityID{Kind: workloadmeta.KindContainer, ID: id},
	}
}

func newKueuePod(uid, containerID, workloadName string) *workloadmeta.KubernetesPod {
	return &workloadmeta.KubernetesPod{
		EntityID: workloadmeta.EntityID{Kind: workloadmeta.KindKubernetesPod, ID: uid},
		EntityMeta: workloadmeta.EntityMeta{
			Name:        uid,
			Namespace:   "default",
			Annotations: map[string]string{kubernetes.KueueWorkloadAnnotationKey: workloadName},
		},
		Containers: []workloadmeta.OrchestratorContainer{{ID: containerID, Name: "main"}},
	}
}

func newKueueWorkload(name, flavor string) *workloadmeta.KubernetesKueueWorkload {
	return &workloadmeta.KubernetesKueueWorkload{
		EntityID: workloadmeta.EntityID{
			Kind: workloadmeta.KindKubernetesKueueWorkload,
			ID:   workloadmeta.GenerateKueueWorkloadEntityID("default", name),
		},
		EntityMeta: workloadmeta.EntityMeta{Name: name, Namespace: "default", UID: name + "-uid"},
		PodSetAssignments: []workloadmeta.KueuePodSetAssignment{
			{Name: "main", Flavors: map[string]string{"nvidia.com/gpu": flavor}},
		},
	}
}

func newKueueResourceFlavor(name, gpuFamily string) *workloadmeta.KubernetesKueueResourceFlavor {
	return &workloadmeta.KubernetesKueueResourceFlavor{
		EntityID: workloadmeta.EntityID{
			Kind: workloadmeta.KindKubernetesKueueResourceFlavor,
			ID:   workloadmeta.GenerateKueueResourceFlavorEntityID(name),
		},
		EntityMeta:         workloadmeta.EntityMeta{Name: name},
		NodeAffinityLabels: map[string]string{"nvidia.com/gpu.family": gpuFamily},
	}
}

func newKAIPodGroup(name, queue string) *workloadmeta.KubernetesKAIPodGroup {
	return &workloadmeta.KubernetesKAIPodGroup{
		EntityID: workloadmeta.EntityID{
			Kind: workloadmeta.KindKubernetesKAIPodGroup,
			ID:   workloadmeta.GenerateKAIPodGroupEntityID("default", name),
		},
		EntityMeta: workloadmeta.EntityMeta{Name: name, Namespace: "default"},
		QueueName:  queue,
	}
}

func kaiTags(tagInfo *types.TagInfo) []string {
	if tagInfo == nil {
		return nil
	}
	var kai []string
	for _, tag := range append(tagInfo.LowCardTags, tagInfo.OrchestratorCardTags...) {
		if strings.HasPrefix(tag, "kai_") {
			kai = append(kai, tag)
		}
	}
	return kai
}

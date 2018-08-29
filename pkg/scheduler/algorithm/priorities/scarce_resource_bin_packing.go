/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package priorities

import (
	"fmt"

	"k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/scheduler/algorithm"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/api"
	schedulercache "k8s.io/kubernetes/pkg/scheduler/cache"
)

// ScarceResourceBinPacking contains information to calculate bin packing priority.
type ScarceResourceBinPacking struct {
	scarceResource string
}

// NewScarceResourceBinPacking creates a ScarceResourceBinPackingPriorityMap.
func NewScarceResourceBinPacking(scarceResource string) (algorithm.PriorityMapFunction, algorithm.PriorityReduceFunction) {
	scarceResourceBinPackingPrioritizer := &ScarceResourceBinPacking{
		scarceResource: scarceResource,
	}
	return scarceResourceBinPackingPrioritizer.ScarceResourceBinPackingPriorityMap, nil
}

// ScarceResourceBinPackingPriorityMap is a priority function that favors nodes that have higher utlization of scare resource.
// It will detect whether the requested scarce resource is present on a node, and then calculate a score ranging from 0 to 10
// based total utlization (best fit)
// - If none of the scare resource are requested, this node will be given the lowest priority.
// - If the scarce resource is requested, the larger the resource utlization ratio, the higher the node's priority.
func (s *ScarceResourceBinPacking) ScarceResourceBinPackingPriorityMap(pod *v1.Pod, meta interface{}, nodeInfo *schedulercache.NodeInfo) (schedulerapi.HostPriority, error) {
	var score int
	node := nodeInfo.Node()
	if node == nil {
		return schedulerapi.HostPriority{}, fmt.Errorf("node not found")
	}
	if !podRequestsResource(*pod, s.scarceResource) {
		score = 0
	} else {
		score = calculateScareResourceScore(nodeInfo, pod.Spec.Containers, s.scarceResource)
	}

	return schedulerapi.HostPriority{
		Host:  node.Name,
		Score: score,
	}, nil
}

// calculateScareResourceScore returns total utlization of the scare resource on the node
func calculateScareResourceScore(nodeInfo *schedulercache.NodeInfo, containers []v1.Container, resource string) int {
	reqResource := 0
	usedResource := 0
	for _, container := range containers {
		if qunatity, ok := container.Resources.Requests[v1.ResourceName(resource)]; ok {
			reqResource += int(qunatity.Value())
		}
	}
	for _, pod := range nodeInfo.Pods() {
		for _, container := range pod.Spec.Containers {
			if qunatity, ok := container.Resources.Requests[v1.ResourceName(resource)]; ok {
				usedResource += int(qunatity.Value())
			}
		}
	}

	available := int(nodeInfo.AllocatableResource().ScalarResources[v1.ResourceName(resource)])
	return ((usedResource + reqResource) * schedulerapi.MaxPriority) / available
}

// podRequestsResource checks if a given pod requests the scare resource. if false the priority is set to 0
func podRequestsResource(pod v1.Pod, resource string) bool {
	containerRequestsResource := func(container v1.Container) bool {
		for resName, quantity := range container.Resources.Requests {
			if string(resName) == resource && quantity.MilliValue() > 0 {
				return true
			}
		}
		for resName, quantity := range container.Resources.Limits {
			if string(resName) == resource && quantity.MilliValue() > 0 {
				return true
			}
		}
		return false
	}

	for _, c := range pod.Spec.InitContainers {
		if containerRequestsResource(c) {
			return true
		}
	}
	for _, c := range pod.Spec.Containers {
		if containerRequestsResource(c) {
			return true
		}
	}
	return false
}

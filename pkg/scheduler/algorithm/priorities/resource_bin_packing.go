/*
Copyright 2018 The Kubernetes Authors.

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

// ResourceBinPacking contains information to calculate bin packing priority.
type ResourceBinPacking struct {
	resource v1.ResourceName
}

// NewResourceBinPacking creates a ResourceBinPackingPriorityMap.
func NewResourceBinPacking(resource v1.ResourceName) (algorithm.PriorityMapFunction, algorithm.PriorityReduceFunction) {
	resourceBinPackingPrioritizer := &ResourceBinPacking{
		resource: resource,
	}
	return resourceBinPackingPrioritizer.ResourceBinPackingPriorityMap, nil
}

// ResourceBinPackingPriorityDefault creates a requestedToCapacity based
// ResourceAllocationPriority using default resource scoring function shape.
// The default function assigns 1.0 to resource when all capacity is available
// and 0.0 when requested amount is equal to capacity.
func ResourceBinPackingPriorityDefault() *ResourceBinPacking {
	defaultResourceBinPackingPrioritizer := &ResourceBinPacking{
		resource: "",
	}
	return defaultResourceBinPackingPrioritizer
}

// ResourceBinPackingPriorityMap is a priority function that favors nodes that have higher utlization of scare resource.
// It will detect whether the requested scarce resource is present on a node, and then calculate a score ranging from 0 to 10
// based total utlization (best fit)
// - If none of the scare resource are requested, this node will be given the lowest priority.
// - If the scarce resource is requested, the larger the resource utlization ratio, the higher the node's priority.
func (r *ResourceBinPacking) ResourceBinPackingPriorityMap(pod *v1.Pod, meta interface{}, nodeInfo *schedulercache.NodeInfo) (schedulerapi.HostPriority, error) {
	var score int
	node := nodeInfo.Node()
	if len(r.resource) == 0 {
		return schedulerapi.HostPriority{}, fmt.Errorf("resource not defined")
	}
	if node == nil {
		return schedulerapi.HostPriority{}, fmt.Errorf("node not found")
	}
	if !podRequestsResource(*pod, r.resource) {
		score = 0
	} else {
		score = int(calculateScareResourceScore(nodeInfo, pod, r.resource))
	}

	return schedulerapi.HostPriority{
		Host:  node.Name,
		Score: score,
	}, nil
}

// calculateScareResourceScore returns total utlization of the scare resource on the node
func calculateScareResourceScore(nodeInfo *schedulercache.NodeInfo, pod *v1.Pod, resource v1.ResourceName) int64 {
	reqResource := int64(0)
	usedResource := int64(0)
	if resource == "cpu" {
		usedResource = nodeInfo.RequestedResource().MilliCPU
	} else if resource == "storage" {
		usedResource = nodeInfo.RequestedResource().Memory
	} else if resource == "ephemeral-storage" {
		usedResource = nodeInfo.RequestedResource().EphemeralStorage
	} else {
		usedResource = nodeInfo.RequestedResource().ScalarResources[resource]
	}
	reqResourceInit := int64(0)
	for _, container := range pod.Spec.Containers {
		if qunatity, ok := container.Resources.Requests[resource]; ok {
			reqResource += qunatity.Value()
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if qunatity, ok := container.Resources.Requests[resource]; ok {
			reqResourceInit += qunatity.Value()
		}
	}
	if reqResourceInit > reqResource {
		reqResource = reqResourceInit
	}
	available := nodeInfo.AllocatableResource().ScalarResources[resource]
	return ((usedResource + reqResource) * schedulerapi.MaxPriority) / available
}

// podRequestsResource checks if a given pod requests the scare resource. if false the priority is set to 0
func podRequestsResource(pod v1.Pod, resource v1.ResourceName) bool {
	containerRequestsResource := func(container v1.Container) bool {
		for resName, quantity := range container.Resources.Requests {
			if resName == resource && quantity.MilliValue() > 0 {
				return true
			}
		}
		for resName, quantity := range container.Resources.Limits {
			if resName == resource && quantity.MilliValue() > 0 {
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

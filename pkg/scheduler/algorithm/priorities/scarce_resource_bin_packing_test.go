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
	"reflect"
	"testing"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/api"
	schedulercache "k8s.io/kubernetes/pkg/scheduler/cache"
)

func TestScarceResourceBinPacking(t *testing.T) {
	scarceResource := "intel.com/foo"
	noResources := v1.PodSpec{
		Containers: []v1.Container{},
	}
	scareResourcePod1 := v1.PodSpec{
		Containers: []v1.Container{
			{
				Resources: v1.ResourceRequirements{
					Requests: v1.ResourceList{
						v1.ResourceName(scarceResource): resource.MustParse("2"),
					},
				},
			},
		},
	}
	scareResourcePod2 := v1.PodSpec{
		Containers: []v1.Container{
			{
				Resources: v1.ResourceRequirements{
					Requests: v1.ResourceList{
						v1.ResourceName(scarceResource): resource.MustParse("4"),
					},
				},
			},
		},
	}
	machine2Pod := scareResourcePod1
	machine2Pod.NodeName = "machine2"
	tests := []struct {
		pod          *v1.Pod
		pods         []*v1.Pod
		nodes        []*v1.Node
		expectedList schedulerapi.HostPriorityList
		name         string
	}{
		{
			/*
				Node1 scores (used resources) on 0-10 scale
				used + requested / available
				Node1 Score: { (0 + 0) / 4 } * 10 = 0

				Node2 scores (used resources) on 0-10 scale
				used + requested / available
				Node2 Score: { (0 + 0) / 8 } * 10 = 0
			*/
			pod:          &v1.Pod{Spec: noResources},
			nodes:        []*v1.Node{makeNodeScarceResource("machine1", 4000, 10000, scarceResource, 8), makeNodeScarceResource("machine2", 4000, 10000, scarceResource, 4)},
			expectedList: []schedulerapi.HostPriority{{Host: "machine1", Score: 0}, {Host: "machine2", Score: 0}},
			name:         "nothing scheduled, nothing requested",
		},

		{
			/*
				Node1 scores (used resources) on 0-10 scale
				used + requested / available
				Node1 Score: { (0 + 2) / 8 } * 10 = 2

				Node2 scores (used resources) on 0-10 scale
				used + requested / available
				Node2 Score: { (0 + 2) / 4 } * 10 = 5
			*/
			pod:          &v1.Pod{Spec: scareResourcePod1},
			nodes:        []*v1.Node{makeNodeScarceResource("machine1", 4000, 10000, scarceResource, 8), makeNodeScarceResource("machine2", 4000, 10000, scarceResource, 4)},
			expectedList: []schedulerapi.HostPriority{{Host: "machine1", Score: 2}, {Host: "machine2", Score: 5}},
			name:         "resources requested, pods scheduled with less resources",
			pods: []*v1.Pod{
				{Spec: noResources},
			},
		},

		{
			/*
				Node1 scores (used resources) on 0-10 scale
				used + requested / available
				Node1 Score: { (0 + 2) / 8 } * 10 = 2

				Node2 scores (used resources) on 0-10 scale
				used + requested / available
				Node2 Score: { (2 + 2) / 4 } * 10 = 10
			*/
			pod:          &v1.Pod{Spec: scareResourcePod1},
			nodes:        []*v1.Node{makeNodeScarceResource("machine1", 4000, 10000, scarceResource, 8), makeNodeScarceResource("machine2", 4000, 10000, scarceResource, 4)},
			expectedList: []schedulerapi.HostPriority{{Host: "machine1", Score: 2}, {Host: "machine2", Score: 10}},
			name:         "resources requested, pods scheduled with resources, on node with existing pod running ",
			pods: []*v1.Pod{
				{Spec: machine2Pod},
			},
		},

		{
			/*
				Node1 scores (used resources) on 0-10 scale
				used + requested / available
				Node1 Score: { (0 + 4) / 8 } * 10 = 5

				Node2 scores (used resources) on 0-10 scale
				used + requested / available
				Node2 Score: { (0 + 4) / 4 } * 10 = 10
			*/
			pod:          &v1.Pod{Spec: scareResourcePod2},
			nodes:        []*v1.Node{makeNodeScarceResource("machine1", 4000, 10000, scarceResource, 8), makeNodeScarceResource("machine2", 4000, 10000, scarceResource, 4)},
			expectedList: []schedulerapi.HostPriority{{Host: "machine1", Score: 5}, {Host: "machine2", Score: 10}},
			name:         "resources requested, pods scheduled with more resources",
			pods: []*v1.Pod{
				{Spec: noResources},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nodeNameToInfo := schedulercache.CreateNodeNameToInfoMap(test.pods, test.nodes)
			prior, _ := NewScarceResourceBinPacking(scarceResource)
			list, err := priorityFunction(prior, nil, nil)(test.pod, nodeNameToInfo, test.nodes)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(test.expectedList, list) {
				t.Errorf("expected %#v, got %#v", test.expectedList, list)
			}
		})
	}
}

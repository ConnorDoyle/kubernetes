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

package kubelet

import (
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	cadvisorapiv2 "github.com/google/cadvisor/info/v2"
	"k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/kubernetes/pkg/kubelet/cm"
	kubecontainer "k8s.io/kubernetes/pkg/kubelet/container"
	"k8s.io/kubernetes/pkg/kubelet/util/sliceutils"
	"k8s.io/kubernetes/pkg/version"
	"k8s.io/kubernetes/pkg/volume/util/volumehelper"
)

const (
	maxImageTagsForTest = 20
)

// generateTestingImageList generate randomly generated image list and corresponding expectedImageList.
func generateTestingImageList(count int) ([]kubecontainer.Image, []v1.ContainerImage) {
	// imageList is randomly generated image list
	var imageList []kubecontainer.Image
	for ; count > 0; count-- {
		imageItem := kubecontainer.Image{
			ID:       string(uuid.NewUUID()),
			RepoTags: generateImageTags(),
			Size:     rand.Int63nRange(minImgSize, maxImgSize+1),
		}
		imageList = append(imageList, imageItem)
	}

	// expectedImageList is generated by imageList according to size and maxImagesInNodeStatus
	// 1. sort the imageList by size
	sort.Sort(sliceutils.ByImageSize(imageList))
	// 2. convert sorted imageList to v1.ContainerImage list
	var expectedImageList []v1.ContainerImage
	for _, kubeImage := range imageList {
		apiImage := v1.ContainerImage{
			Names:     kubeImage.RepoTags[0:maxNamesPerImageInNodeStatus],
			SizeBytes: kubeImage.Size,
		}

		expectedImageList = append(expectedImageList, apiImage)
	}
	// 3. only returns the top maxImagesInNodeStatus images in expectedImageList
	return imageList, expectedImageList[0:maxImagesInNodeStatus]
}

func generateImageTags() []string {
	var tagList []string
	// Generate > maxNamesPerImageInNodeStatus tags so that the test can verify
	// that kubelet report up to maxNamesPerImageInNodeStatus tags.
	count := rand.IntnRange(maxNamesPerImageInNodeStatus+1, maxImageTagsForTest+1)
	for ; count > 0; count-- {
		tagList = append(tagList, "gcr.io/google_containers:v"+strconv.Itoa(count))
	}
	return tagList
}

func applyNodeStatusPatch(originalNode *v1.Node, patch []byte) (*v1.Node, error) {
	original, err := json.Marshal(originalNode)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal original node %#v: %v", originalNode, err)
	}
	updated, err := strategicpatch.StrategicMergePatch(original, patch, v1.Node{})
	if err != nil {
		return nil, fmt.Errorf("failed to apply strategic merge patch %q on node %#v: %v",
			patch, originalNode, err)
	}
	updatedNode := &v1.Node{}
	if err := json.Unmarshal(updated, updatedNode); err != nil {
		return nil, fmt.Errorf("failed to unmarshal updated node %q: %v", updated, err)
	}
	return updatedNode, nil
}

type localCM struct {
	cm.ContainerManager
	allocatable v1.ResourceList
	capacity    v1.ResourceList
}

func (lcm *localCM) GetNodeAllocatableReservation() v1.ResourceList {
	return lcm.allocatable
}

func (lcm *localCM) GetCapacity() v1.ResourceList {
	return lcm.capacity
}

func TestUpdateNewNodeStatus(t *testing.T) {
	// generate one more than maxImagesInNodeStatus in inputImageList
	inputImageList, expectedImageList := generateTestingImageList(maxImagesInNodeStatus + 1)
	testKubelet := newTestKubeletWithImageList(
		t, inputImageList, false /* controllerAttachDetachEnabled */)
	defer testKubelet.Cleanup()
	kubelet := testKubelet.kubelet
	kubelet.containerManager = &localCM{
		ContainerManager: cm.NewStubContainerManager(),
		allocatable: v1.ResourceList{
			v1.ResourceCPU:    *resource.NewMilliQuantity(200, resource.DecimalSI),
			v1.ResourceMemory: *resource.NewQuantity(100E6, resource.BinarySI),
		},
		capacity: v1.ResourceList{
			v1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
			v1.ResourceMemory: *resource.NewQuantity(10E9, resource.BinarySI),
		},
	}
	kubeClient := testKubelet.fakeKubeClient
	existingNode := v1.Node{ObjectMeta: metav1.ObjectMeta{Name: testKubeletHostname}}
	kubeClient.ReactionChain = fake.NewSimpleClientset(&v1.NodeList{Items: []v1.Node{existingNode}}).ReactionChain
	machineInfo := &cadvisorapi.MachineInfo{
		MachineID:      "123",
		SystemUUID:     "abc",
		BootID:         "1b3",
		NumCores:       2,
		MemoryCapacity: 10E9, // 10G
	}
	mockCadvisor := testKubelet.fakeCadvisor
	mockCadvisor.On("Start").Return(nil)
	mockCadvisor.On("MachineInfo").Return(machineInfo, nil)
	versionInfo := &cadvisorapi.VersionInfo{
		KernelVersion:      "3.16.0-0.bpo.4-amd64",
		ContainerOsVersion: "Debian GNU/Linux 7 (wheezy)",
	}
	mockCadvisor.On("VersionInfo").Return(versionInfo, nil)

	expectedNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: testKubeletHostname},
		Spec:       v1.NodeSpec{},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{
				{
					Type:               v1.NodeOutOfDisk,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientDisk",
					Message:            fmt.Sprintf("kubelet has sufficient disk space available"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeMemoryPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientMemory",
					Message:            fmt.Sprintf("kubelet has sufficient memory available"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeDiskPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasNoDiskPressure",
					Message:            fmt.Sprintf("kubelet has no disk pressure"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeCPUPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasNoCPUPressure",
					Message:            fmt.Sprintf("kubelet has no CPU pressure"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeReady,
					Status:             v1.ConditionTrue,
					Reason:             "KubeletReady",
					Message:            fmt.Sprintf("kubelet is posting ready status"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
			},
			NodeInfo: v1.NodeSystemInfo{
				MachineID:               "123",
				SystemUUID:              "abc",
				BootID:                  "1b3",
				KernelVersion:           "3.16.0-0.bpo.4-amd64",
				OSImage:                 "Debian GNU/Linux 7 (wheezy)",
				OperatingSystem:         goruntime.GOOS,
				Architecture:            goruntime.GOARCH,
				ContainerRuntimeVersion: "test://1.5.0",
				KubeletVersion:          version.Get().String(),
				KubeProxyVersion:        version.Get().String(),
			},
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(10E9, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(1800, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(9900E6, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
			Addresses: []v1.NodeAddress{
				{Type: v1.NodeInternalIP, Address: "127.0.0.1"},
				{Type: v1.NodeHostName, Address: testKubeletHostname},
			},
			Images: expectedImageList,
		},
	}

	kubelet.updateRuntimeUp()
	assert.NoError(t, kubelet.updateNodeStatus())
	actions := kubeClient.Actions()
	require.Len(t, actions, 2)
	require.True(t, actions[1].Matches("patch", "nodes"))
	require.Equal(t, actions[1].GetSubresource(), "status")

	updatedNode, err := applyNodeStatusPatch(&existingNode, actions[1].(core.PatchActionImpl).GetPatch())
	assert.NoError(t, err)
	for i, cond := range updatedNode.Status.Conditions {
		assert.False(t, cond.LastHeartbeatTime.IsZero(), "LastHeartbeatTime for %v condition is zero", cond.Type)
		assert.False(t, cond.LastTransitionTime.IsZero(), "LastTransitionTime for %v condition  is zero", cond.Type)
		updatedNode.Status.Conditions[i].LastHeartbeatTime = metav1.Time{}
		updatedNode.Status.Conditions[i].LastTransitionTime = metav1.Time{}
	}

	// Version skew workaround. See: https://github.com/kubernetes/kubernetes/issues/16961
	assert.Equal(t, v1.NodeReady, updatedNode.Status.Conditions[len(updatedNode.Status.Conditions)-1].Type, "NotReady should be last")
	assert.Len(t, updatedNode.Status.Images, maxImagesInNodeStatus)
	assert.True(t, apiequality.Semantic.DeepEqual(expectedNode, updatedNode), "%s", diff.ObjectDiff(expectedNode, updatedNode))
}

func TestUpdateExistingNodeStatus(t *testing.T) {
	testKubelet := newTestKubelet(t, false /* controllerAttachDetachEnabled */)
	defer testKubelet.Cleanup()
	kubelet := testKubelet.kubelet
	kubelet.containerManager = &localCM{
		ContainerManager: cm.NewStubContainerManager(),
		allocatable: v1.ResourceList{
			v1.ResourceCPU:    *resource.NewMilliQuantity(200, resource.DecimalSI),
			v1.ResourceMemory: *resource.NewQuantity(100E6, resource.BinarySI),
		},
		capacity: v1.ResourceList{
			v1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
			v1.ResourceMemory: *resource.NewQuantity(20E9, resource.BinarySI),
		},
	}

	kubeClient := testKubelet.fakeKubeClient
	existingNode := v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: testKubeletHostname},
		Spec:       v1.NodeSpec{},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{
				{
					Type:               v1.NodeOutOfDisk,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientDisk",
					Message:            fmt.Sprintf("kubelet has sufficient disk space available"),
					LastHeartbeatTime:  metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
					LastTransitionTime: metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
				},
				{
					Type:               v1.NodeMemoryPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientMemory",
					Message:            fmt.Sprintf("kubelet has sufficient memory available"),
					LastHeartbeatTime:  metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
					LastTransitionTime: metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
				},
				{
					Type:               v1.NodeDiskPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientDisk",
					Message:            fmt.Sprintf("kubelet has sufficient disk space available"),
					LastHeartbeatTime:  metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
					LastTransitionTime: metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
				},
				{
					Type:               v1.NodeCPUPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientCPU",
					Message:            fmt.Sprintf("kubelet has no CPU pressure"),
					LastHeartbeatTime:  metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
					LastTransitionTime: metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
				},
				{
					Type:               v1.NodeReady,
					Status:             v1.ConditionTrue,
					Reason:             "KubeletReady",
					Message:            fmt.Sprintf("kubelet is posting ready status"),
					LastHeartbeatTime:  metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
					LastTransitionTime: metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC),
				},
			},
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(3000, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(20E9, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(2800, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(19900E6, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
		},
	}
	kubeClient.ReactionChain = fake.NewSimpleClientset(&v1.NodeList{Items: []v1.Node{existingNode}}).ReactionChain
	mockCadvisor := testKubelet.fakeCadvisor
	mockCadvisor.On("Start").Return(nil)
	machineInfo := &cadvisorapi.MachineInfo{
		MachineID:      "123",
		SystemUUID:     "abc",
		BootID:         "1b3",
		NumCores:       2,
		MemoryCapacity: 20E9,
	}
	mockCadvisor.On("MachineInfo").Return(machineInfo, nil)
	versionInfo := &cadvisorapi.VersionInfo{
		KernelVersion:      "3.16.0-0.bpo.4-amd64",
		ContainerOsVersion: "Debian GNU/Linux 7 (wheezy)",
	}
	mockCadvisor.On("VersionInfo").Return(versionInfo, nil)

	expectedNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: testKubeletHostname},
		Spec:       v1.NodeSpec{},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{
				{
					Type:               v1.NodeOutOfDisk,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientDisk",
					Message:            fmt.Sprintf("kubelet has sufficient disk space available"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeMemoryPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientMemory",
					Message:            fmt.Sprintf("kubelet has sufficient memory available"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeDiskPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientDisk",
					Message:            fmt.Sprintf("kubelet has sufficient disk space available"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeCPUPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientCPU",
					Message:            fmt.Sprintf("kubelet has no CPU pressure"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeReady,
					Status:             v1.ConditionTrue,
					Reason:             "KubeletReady",
					Message:            fmt.Sprintf("kubelet is posting ready status"),
					LastHeartbeatTime:  metav1.Time{}, // placeholder
					LastTransitionTime: metav1.Time{}, // placeholder
				},
			},
			NodeInfo: v1.NodeSystemInfo{
				MachineID:               "123",
				SystemUUID:              "abc",
				BootID:                  "1b3",
				KernelVersion:           "3.16.0-0.bpo.4-amd64",
				OSImage:                 "Debian GNU/Linux 7 (wheezy)",
				OperatingSystem:         goruntime.GOOS,
				Architecture:            goruntime.GOARCH,
				ContainerRuntimeVersion: "test://1.5.0",
				KubeletVersion:          version.Get().String(),
				KubeProxyVersion:        version.Get().String(),
			},
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(20E9, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(1800, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(19900E6, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
			Addresses: []v1.NodeAddress{
				{Type: v1.NodeInternalIP, Address: "127.0.0.1"},
				{Type: v1.NodeHostName, Address: testKubeletHostname},
			},
			// images will be sorted from max to min in node status.
			Images: []v1.ContainerImage{
				{
					Names:     []string{"gcr.io/google_containers:v3", "gcr.io/google_containers:v4"},
					SizeBytes: 456,
				},
				{
					Names:     []string{"gcr.io/google_containers:v1", "gcr.io/google_containers:v2"},
					SizeBytes: 123,
				},
			},
		},
	}

	kubelet.updateRuntimeUp()
	assert.NoError(t, kubelet.updateNodeStatus())

	actions := kubeClient.Actions()
	assert.Len(t, actions, 2)

	assert.IsType(t, core.PatchActionImpl{}, actions[1])
	patchAction := actions[1].(core.PatchActionImpl)

	updatedNode, err := applyNodeStatusPatch(&existingNode, patchAction.GetPatch())
	require.NoError(t, err)

	for i, cond := range updatedNode.Status.Conditions {
		old := metav1.Date(2012, 1, 1, 0, 0, 0, 0, time.UTC).Time
		// Expect LastHearbeat to be updated to Now, while LastTransitionTime to be the same.
		assert.NotEqual(t, old, cond.LastHeartbeatTime.Rfc3339Copy().UTC(), "LastHeartbeatTime for condition %v", cond.Type)
		assert.EqualValues(t, old, cond.LastTransitionTime.Rfc3339Copy().UTC(), "LastTransitionTime for condition %v", cond.Type)

		updatedNode.Status.Conditions[i].LastHeartbeatTime = metav1.Time{}
		updatedNode.Status.Conditions[i].LastTransitionTime = metav1.Time{}
	}

	// Version skew workaround. See: https://github.com/kubernetes/kubernetes/issues/16961
	assert.Equal(t, v1.NodeReady, updatedNode.Status.Conditions[len(updatedNode.Status.Conditions)-1].Type,
		"NodeReady should be the last condition")
	assert.True(t, apiequality.Semantic.DeepEqual(expectedNode, updatedNode), "%s", diff.ObjectDiff(expectedNode, updatedNode))
}

func TestUpdateNodeStatusWithRuntimeStateError(t *testing.T) {
	testKubelet := newTestKubelet(t, false /* controllerAttachDetachEnabled */)
	defer testKubelet.Cleanup()
	kubelet := testKubelet.kubelet
	kubelet.containerManager = &localCM{
		ContainerManager: cm.NewStubContainerManager(),
		allocatable: v1.ResourceList{
			v1.ResourceCPU:    *resource.NewMilliQuantity(200, resource.DecimalSI),
			v1.ResourceMemory: *resource.NewQuantity(100E6, resource.BinarySI),
		},
		capacity: v1.ResourceList{
			v1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
			v1.ResourceMemory: *resource.NewQuantity(10E9, resource.BinarySI),
		},
	}

	clock := testKubelet.fakeClock
	kubeClient := testKubelet.fakeKubeClient
	existingNode := v1.Node{ObjectMeta: metav1.ObjectMeta{Name: testKubeletHostname}}
	kubeClient.ReactionChain = fake.NewSimpleClientset(&v1.NodeList{Items: []v1.Node{existingNode}}).ReactionChain
	mockCadvisor := testKubelet.fakeCadvisor
	mockCadvisor.On("Start").Return(nil)
	machineInfo := &cadvisorapi.MachineInfo{
		MachineID:      "123",
		SystemUUID:     "abc",
		BootID:         "1b3",
		NumCores:       2,
		MemoryCapacity: 10E9,
	}
	mockCadvisor.On("MachineInfo").Return(machineInfo, nil)
	versionInfo := &cadvisorapi.VersionInfo{
		KernelVersion:      "3.16.0-0.bpo.4-amd64",
		ContainerOsVersion: "Debian GNU/Linux 7 (wheezy)",
	}
	mockCadvisor.On("VersionInfo").Return(versionInfo, nil)

	expectedNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: testKubeletHostname},
		Spec:       v1.NodeSpec{},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{
				{
					Type:               v1.NodeOutOfDisk,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientDisk",
					Message:            fmt.Sprintf("kubelet has sufficient disk space available"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeMemoryPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasSufficientMemory",
					Message:            fmt.Sprintf("kubelet has sufficient memory available"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeDiskPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasNoDiskPressure",
					Message:            fmt.Sprintf("kubelet has no disk pressure"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{
					Type:               v1.NodeCPUPressure,
					Status:             v1.ConditionFalse,
					Reason:             "KubeletHasNoCPUPressure",
					Message:            fmt.Sprintf("kubelet has no CPU pressure"),
					LastHeartbeatTime:  metav1.Time{},
					LastTransitionTime: metav1.Time{},
				},
				{}, //placeholder
			},
			NodeInfo: v1.NodeSystemInfo{
				MachineID:               "123",
				SystemUUID:              "abc",
				BootID:                  "1b3",
				KernelVersion:           "3.16.0-0.bpo.4-amd64",
				OSImage:                 "Debian GNU/Linux 7 (wheezy)",
				OperatingSystem:         goruntime.GOOS,
				Architecture:            goruntime.GOARCH,
				ContainerRuntimeVersion: "test://1.5.0",
				KubeletVersion:          version.Get().String(),
				KubeProxyVersion:        version.Get().String(),
			},
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(10E9, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(1800, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(9900E6, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
			Addresses: []v1.NodeAddress{
				{Type: v1.NodeInternalIP, Address: "127.0.0.1"},
				{Type: v1.NodeHostName, Address: testKubeletHostname},
			},
			Images: []v1.ContainerImage{
				{
					Names:     []string{"gcr.io/google_containers:v3", "gcr.io/google_containers:v4"},
					SizeBytes: 456,
				},
				{
					Names:     []string{"gcr.io/google_containers:v1", "gcr.io/google_containers:v2"},
					SizeBytes: 123,
				},
			},
		},
	}

	checkNodeStatus := func(status v1.ConditionStatus, reason string) {
		kubeClient.ClearActions()
		assert.NoError(t, kubelet.updateNodeStatus())
		actions := kubeClient.Actions()
		require.Len(t, actions, 2)
		require.True(t, actions[1].Matches("patch", "nodes"))
		require.Equal(t, actions[1].GetSubresource(), "status")

		updatedNode, err := applyNodeStatusPatch(&existingNode, actions[1].(core.PatchActionImpl).GetPatch())
		require.NoError(t, err, "can't apply node status patch")

		for i, cond := range updatedNode.Status.Conditions {
			assert.False(t, cond.LastHeartbeatTime.IsZero(), "LastHeartbeatTime for %v condition is zero", cond.Type)
			assert.False(t, cond.LastTransitionTime.IsZero(), "LastTransitionTime for %v condition  is zero", cond.Type)
			updatedNode.Status.Conditions[i].LastHeartbeatTime = metav1.Time{}
			updatedNode.Status.Conditions[i].LastTransitionTime = metav1.Time{}
		}

		// Version skew workaround. See: https://github.com/kubernetes/kubernetes/issues/16961
		lastIndex := len(updatedNode.Status.Conditions) - 1

		assert.Equal(t, v1.NodeReady, updatedNode.Status.Conditions[lastIndex].Type, "NodeReady should be the last condition")
		assert.NotEmpty(t, updatedNode.Status.Conditions[lastIndex].Message)

		updatedNode.Status.Conditions[lastIndex].Message = ""
		expectedNode.Status.Conditions[lastIndex] = v1.NodeCondition{
			Type:               v1.NodeReady,
			Status:             status,
			Reason:             reason,
			LastHeartbeatTime:  metav1.Time{},
			LastTransitionTime: metav1.Time{},
		}
		assert.True(t, apiequality.Semantic.DeepEqual(expectedNode, updatedNode), "%s", diff.ObjectDiff(expectedNode, updatedNode))
	}

	// TODO(random-liu): Refactor the unit test to be table driven test.
	// Should report kubelet not ready if the runtime check is out of date
	clock.SetTime(time.Now().Add(-maxWaitForContainerRuntime))
	kubelet.updateRuntimeUp()
	checkNodeStatus(v1.ConditionFalse, "KubeletNotReady")

	// Should report kubelet ready if the runtime check is updated
	clock.SetTime(time.Now())
	kubelet.updateRuntimeUp()
	checkNodeStatus(v1.ConditionTrue, "KubeletReady")

	// Should report kubelet not ready if the runtime check is out of date
	clock.SetTime(time.Now().Add(-maxWaitForContainerRuntime))
	kubelet.updateRuntimeUp()
	checkNodeStatus(v1.ConditionFalse, "KubeletNotReady")

	// Should report kubelet not ready if the runtime check failed
	fakeRuntime := testKubelet.fakeRuntime
	// Inject error into fake runtime status check, node should be NotReady
	fakeRuntime.StatusErr = fmt.Errorf("injected runtime status error")
	clock.SetTime(time.Now())
	kubelet.updateRuntimeUp()
	checkNodeStatus(v1.ConditionFalse, "KubeletNotReady")

	fakeRuntime.StatusErr = nil

	// Should report node not ready if runtime status is nil.
	fakeRuntime.RuntimeStatus = nil
	kubelet.updateRuntimeUp()
	checkNodeStatus(v1.ConditionFalse, "KubeletNotReady")

	// Should report node not ready if runtime status is empty.
	fakeRuntime.RuntimeStatus = &kubecontainer.RuntimeStatus{}
	kubelet.updateRuntimeUp()
	checkNodeStatus(v1.ConditionFalse, "KubeletNotReady")

	// Should report node not ready if RuntimeReady is false.
	fakeRuntime.RuntimeStatus = &kubecontainer.RuntimeStatus{
		Conditions: []kubecontainer.RuntimeCondition{
			{Type: kubecontainer.RuntimeReady, Status: false},
			{Type: kubecontainer.NetworkReady, Status: true},
		},
	}
	kubelet.updateRuntimeUp()
	checkNodeStatus(v1.ConditionFalse, "KubeletNotReady")

	// Should report node ready if RuntimeReady is true.
	fakeRuntime.RuntimeStatus = &kubecontainer.RuntimeStatus{
		Conditions: []kubecontainer.RuntimeCondition{
			{Type: kubecontainer.RuntimeReady, Status: true},
			{Type: kubecontainer.NetworkReady, Status: true},
		},
	}
	kubelet.updateRuntimeUp()
	checkNodeStatus(v1.ConditionTrue, "KubeletReady")

	// Should report node not ready if NetworkReady is false.
	fakeRuntime.RuntimeStatus = &kubecontainer.RuntimeStatus{
		Conditions: []kubecontainer.RuntimeCondition{
			{Type: kubecontainer.RuntimeReady, Status: true},
			{Type: kubecontainer.NetworkReady, Status: false},
		},
	}
	kubelet.updateRuntimeUp()
	checkNodeStatus(v1.ConditionFalse, "KubeletNotReady")
}

func TestUpdateNodeStatusError(t *testing.T) {
	testKubelet := newTestKubelet(t, false /* controllerAttachDetachEnabled */)
	defer testKubelet.Cleanup()
	kubelet := testKubelet.kubelet
	// No matching node for the kubelet
	testKubelet.fakeKubeClient.ReactionChain = fake.NewSimpleClientset(&v1.NodeList{Items: []v1.Node{}}).ReactionChain
	assert.Error(t, kubelet.updateNodeStatus())
	assert.Len(t, testKubelet.fakeKubeClient.Actions(), nodeStatusUpdateRetry)
}

func TestRegisterWithApiServer(t *testing.T) {
	testKubelet := newTestKubelet(t, false /* controllerAttachDetachEnabled */)
	defer testKubelet.Cleanup()
	kubelet := testKubelet.kubelet
	kubeClient := testKubelet.fakeKubeClient
	kubeClient.AddReactor("create", "nodes", func(action core.Action) (bool, runtime.Object, error) {
		// Return an error on create.
		return true, &v1.Node{}, &apierrors.StatusError{
			ErrStatus: metav1.Status{Reason: metav1.StatusReasonAlreadyExists},
		}
	})
	kubeClient.AddReactor("get", "nodes", func(action core.Action) (bool, runtime.Object, error) {
		// Return an existing (matching) node on get.
		return true, &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: testKubeletHostname},
			Spec:       v1.NodeSpec{ExternalID: testKubeletHostname},
		}, nil
	})
	kubeClient.AddReactor("*", "*", func(action core.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("no reaction implemented for %s", action)
	})
	machineInfo := &cadvisorapi.MachineInfo{
		MachineID:      "123",
		SystemUUID:     "abc",
		BootID:         "1b3",
		NumCores:       2,
		MemoryCapacity: 1024,
	}
	mockCadvisor := testKubelet.fakeCadvisor
	mockCadvisor.On("MachineInfo").Return(machineInfo, nil)
	versionInfo := &cadvisorapi.VersionInfo{
		KernelVersion:      "3.16.0-0.bpo.4-amd64",
		ContainerOsVersion: "Debian GNU/Linux 7 (wheezy)",
		DockerVersion:      "1.5.0",
	}
	mockCadvisor.On("VersionInfo").Return(versionInfo, nil)
	mockCadvisor.On("ImagesFsInfo").Return(cadvisorapiv2.FsInfo{
		Usage:     400,
		Capacity:  1000,
		Available: 600,
	}, nil)
	mockCadvisor.On("RootFsInfo").Return(cadvisorapiv2.FsInfo{
		Usage:    9,
		Capacity: 10,
	}, nil)

	done := make(chan struct{})
	go func() {
		kubelet.registerWithAPIServer()
		done <- struct{}{}
	}()
	select {
	case <-time.After(wait.ForeverTestTimeout):
		assert.Fail(t, "timed out waiting for registration")
	case <-done:
		return
	}
}

func TestTryRegisterWithApiServer(t *testing.T) {
	alreadyExists := &apierrors.StatusError{
		ErrStatus: metav1.Status{Reason: metav1.StatusReasonAlreadyExists},
	}

	conflict := &apierrors.StatusError{
		ErrStatus: metav1.Status{Reason: metav1.StatusReasonConflict},
	}

	newNode := func(cmad bool, externalID string) *v1.Node {
		node := &v1.Node{
			ObjectMeta: metav1.ObjectMeta{},
			Spec: v1.NodeSpec{
				ExternalID: externalID,
			},
		}

		if cmad {
			node.Annotations = make(map[string]string)
			node.Annotations[volumehelper.ControllerManagedAttachAnnotation] = "true"
		}

		return node
	}

	cases := []struct {
		name            string
		newNode         *v1.Node
		existingNode    *v1.Node
		createError     error
		getError        error
		patchError      error
		deleteError     error
		expectedResult  bool
		expectedActions int
		testSavedNode   bool
		savedNodeIndex  int
		savedNodeCMAD   bool
	}{
		{
			name:            "success case - new node",
			newNode:         &v1.Node{},
			expectedResult:  true,
			expectedActions: 1,
		},
		{
			name:            "success case - existing node - no change in CMAD",
			newNode:         newNode(true, "a"),
			createError:     alreadyExists,
			existingNode:    newNode(true, "a"),
			expectedResult:  true,
			expectedActions: 2,
		},
		{
			name:            "success case - existing node - CMAD disabled",
			newNode:         newNode(false, "a"),
			createError:     alreadyExists,
			existingNode:    newNode(true, "a"),
			expectedResult:  true,
			expectedActions: 3,
			testSavedNode:   true,
			savedNodeIndex:  2,
			savedNodeCMAD:   false,
		},
		{
			name:            "success case - existing node - CMAD enabled",
			newNode:         newNode(true, "a"),
			createError:     alreadyExists,
			existingNode:    newNode(false, "a"),
			expectedResult:  true,
			expectedActions: 3,
			testSavedNode:   true,
			savedNodeIndex:  2,
			savedNodeCMAD:   true,
		},
		{
			name:            "success case - external ID changed",
			newNode:         newNode(false, "b"),
			createError:     alreadyExists,
			existingNode:    newNode(false, "a"),
			expectedResult:  false,
			expectedActions: 3,
		},
		{
			name:            "create failed",
			newNode:         newNode(false, "b"),
			createError:     conflict,
			expectedResult:  false,
			expectedActions: 1,
		},
		{
			name:            "get existing node failed",
			newNode:         newNode(false, "a"),
			createError:     alreadyExists,
			getError:        conflict,
			expectedResult:  false,
			expectedActions: 2,
		},
		{
			name:            "update existing node failed",
			newNode:         newNode(false, "a"),
			createError:     alreadyExists,
			existingNode:    newNode(true, "a"),
			patchError:      conflict,
			expectedResult:  false,
			expectedActions: 3,
		},
		{
			name:            "delete existing node failed",
			newNode:         newNode(false, "b"),
			createError:     alreadyExists,
			existingNode:    newNode(false, "a"),
			deleteError:     conflict,
			expectedResult:  false,
			expectedActions: 3,
		},
	}

	notImplemented := func(action core.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("no reaction implemented for %s", action)
	}

	for _, tc := range cases {
		testKubelet := newTestKubelet(t, false /* controllerAttachDetachEnabled is a don't-care for this test */)
		defer testKubelet.Cleanup()
		kubelet := testKubelet.kubelet
		kubeClient := testKubelet.fakeKubeClient

		kubeClient.AddReactor("create", "nodes", func(action core.Action) (bool, runtime.Object, error) {
			return true, nil, tc.createError
		})
		kubeClient.AddReactor("get", "nodes", func(action core.Action) (bool, runtime.Object, error) {
			// Return an existing (matching) node on get.
			return true, tc.existingNode, tc.getError
		})
		kubeClient.AddReactor("patch", "nodes", func(action core.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() == "status" {
				return true, nil, tc.patchError
			}
			return notImplemented(action)
		})
		kubeClient.AddReactor("delete", "nodes", func(action core.Action) (bool, runtime.Object, error) {
			return true, nil, tc.deleteError
		})
		kubeClient.AddReactor("*", "*", func(action core.Action) (bool, runtime.Object, error) {
			return notImplemented(action)
		})

		result := kubelet.tryRegisterWithAPIServer(tc.newNode)
		require.Equal(t, tc.expectedResult, result, "test [%s]", tc.name)

		actions := kubeClient.Actions()
		assert.Len(t, actions, tc.expectedActions, "test [%s]", tc.name)

		if tc.testSavedNode {
			var savedNode *v1.Node

			t.Logf("actions: %v: %+v", len(actions), actions)
			action := actions[tc.savedNodeIndex]
			if action.GetVerb() == "create" {
				createAction := action.(core.CreateAction)
				obj := createAction.GetObject()
				require.IsType(t, &v1.Node{}, obj)
				savedNode = obj.(*v1.Node)
			} else if action.GetVerb() == "patch" {
				patchAction := action.(core.PatchActionImpl)
				var err error
				savedNode, err = applyNodeStatusPatch(tc.existingNode, patchAction.GetPatch())
				require.NoError(t, err)
			}

			actualCMAD, _ := strconv.ParseBool(savedNode.Annotations[volumehelper.ControllerManagedAttachAnnotation])
			assert.Equal(t, tc.savedNodeCMAD, actualCMAD, "test [%s]", tc.name)
		}
	}
}

func TestUpdateNewNodeStatusTooLargeReservation(t *testing.T) {
	// generate one more than maxImagesInNodeStatus in inputImageList
	inputImageList, _ := generateTestingImageList(maxImagesInNodeStatus + 1)
	testKubelet := newTestKubeletWithImageList(
		t, inputImageList, false /* controllerAttachDetachEnabled */)
	defer testKubelet.Cleanup()
	kubelet := testKubelet.kubelet
	kubelet.containerManager = &localCM{
		ContainerManager: cm.NewStubContainerManager(),
		allocatable: v1.ResourceList{
			v1.ResourceCPU: *resource.NewMilliQuantity(40000, resource.DecimalSI),
		},
		capacity: v1.ResourceList{
			v1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
			v1.ResourceMemory: *resource.NewQuantity(10E9, resource.BinarySI),
		},
	}
	kubeClient := testKubelet.fakeKubeClient
	existingNode := v1.Node{ObjectMeta: metav1.ObjectMeta{Name: testKubeletHostname}}
	kubeClient.ReactionChain = fake.NewSimpleClientset(&v1.NodeList{Items: []v1.Node{existingNode}}).ReactionChain
	machineInfo := &cadvisorapi.MachineInfo{
		MachineID:      "123",
		SystemUUID:     "abc",
		BootID:         "1b3",
		NumCores:       2,
		MemoryCapacity: 10E9, // 10G
	}
	mockCadvisor := testKubelet.fakeCadvisor
	mockCadvisor.On("Start").Return(nil)
	mockCadvisor.On("MachineInfo").Return(machineInfo, nil)
	versionInfo := &cadvisorapi.VersionInfo{
		KernelVersion:      "3.16.0-0.bpo.4-amd64",
		ContainerOsVersion: "Debian GNU/Linux 7 (wheezy)",
	}
	mockCadvisor.On("VersionInfo").Return(versionInfo, nil)

	expectedNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: testKubeletHostname},
		Spec:       v1.NodeSpec{},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(10E9, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(0, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(10E9, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(0, resource.DecimalSI),
			},
		},
	}

	kubelet.updateRuntimeUp()
	assert.NoError(t, kubelet.updateNodeStatus())
	actions := kubeClient.Actions()
	require.Len(t, actions, 2)
	require.True(t, actions[1].Matches("patch", "nodes"))
	require.Equal(t, actions[1].GetSubresource(), "status")

	updatedNode, err := applyNodeStatusPatch(&existingNode, actions[1].(core.PatchActionImpl).GetPatch())
	assert.NoError(t, err)
	assert.True(t, apiequality.Semantic.DeepEqual(expectedNode.Status.Allocatable, updatedNode.Status.Allocatable), "%s", diff.ObjectDiff(expectedNode.Status.Allocatable, updatedNode.Status.Allocatable))
}

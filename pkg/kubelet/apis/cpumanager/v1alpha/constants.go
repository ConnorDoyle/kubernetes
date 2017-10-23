/*
Copyright 2017 The Kubernetes Authors.

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

package cpumanager

const (
	// Current version of the API supported by kubelet
	Version = "v1alpha1"

	// CPUManagerPluginDir is the directory in which the CPU Manager plugin shim
	// expects to find plugin sockets.
	//
	// Only privileged pods have access to this path.
	CPUManagerPluginPath = "/var/lib/kubelet/cpumanager-plugins/"

	// CPUManagerPluginSocket is the path of the plugin socket.
	CPUManagerPluginSocket = CPUManagerPluginPath + "plugin.sock"

	// KubeletSocket is the path of the Kubelet socket.
	KubeletSocket = CPUManagerPluginPath + "kubelet.sock"
)

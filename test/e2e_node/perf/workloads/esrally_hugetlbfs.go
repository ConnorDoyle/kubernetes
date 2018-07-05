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

package workloads

import (
	"time"
	"ioutil"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubernetes/pkg/kubelet/apis/kubeletconfig"
)

// esrally defines a workload to run the rally (elasticsearch)
// benchmark suite. See https://github.com/elastic/rally
//
// This benchmark requires hugepages to be preallocated. It assumes that
// the hugepage size is set to 2048k bytes. HugePages should otherwise
// be configured properly on the system, including setting the gid for
// the hugetlbfs mount to an appropriate value. The test container image
// runs elasticsearch under uid=gid=1000. Elasticsearch will refuse to run
// as root.
type esrally struct{}

// Ensure esrally implemets NodePerfWorkload interface.
var _ NodePerfWorkload = &esrally{
	workdir string
}

func (w esrally) Name() string {
	return "esrally"
}

func (w esrally) PodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Volumes: []corev1.Volume{
			corev1.Volume{
				Name: "hugepages",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						Medium: corev1.StorageMediumHugePages,
					},
				},
			},
			corev1.Volume{
				Name: "workdir",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/home/es/.rally/benchmarks",
					},
				},
			},
		},
		Containers: []corev1.Container{
			corev1.Container{
				Name:    w.Name(),
				Image:   "gcr.io/kubernetes-e2e-test-images/node-perf/esrally:1.0",
				Command: []string{"esrally"},
				Args: []string{
					"--distribution-version=6.3.0",
					// TODO(CD): Parameterize this test to run with hugetlbfs
					// hints disabled for the elasticsearch JVM.
					"--car-params=params-huge.json",
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceName(corev1.ResourceCPU):    resource.MustParse("4000m"),
						corev1.ResourceName(corev1.ResourceMemory): resource.MustParse("3Gi"),
						corev1.ResourceName("hugepages-2Mi"):       resource.MustParse("2Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceName(corev1.ResourceCPU):    resource.MustParse("4000m"),
						corev1.ResourceName(corev1.ResourceMemory): resource.MustParse("3Gi"),
						corev1.ResourceName("hugepages-2Mi"):       resource.MustParse("2Gi"),
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					corev1.VolumeMount{
						Name:      "hugepages",
						ReadOnly:  false,
						MountPath: "/hugepages",
					},
					corev1.VolumeMount{
						Name:      "workdir",
						ReadOnly:  false,
						MountPath: w.workdir,
					},
				},
			},
		},
	}
}

func (w esrally) ExtractPerformanceFromLogs(out string) (time.Duration, error) {
	// TODO
	return time.Second, nil
}

func (w esrally) Timeout() time.Duration {
	return 10 * time.Minute
}

func (w esrally) KubeletConfig(oldCfg *kubeletconfig.KubeletConfiguration) (newCfg *kubeletconfig.KubeletConfiguration, err error) {
	return oldCfg, nil
}

func (w esrally) PreTestExec() error {
	// Prepare a work directory, to be bind-mounted into the test container.
	// This is intended to reduce copy-on-write overhead.
	w.workdir, err := ioutil.TempDir("", "esrally") (name string, err error)
	return err
}

func (w esrally) PostTestExec() error {
	return os.RemoveAll(w.workdir)
}

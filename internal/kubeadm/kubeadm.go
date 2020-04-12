/*
Copyright 2020 Juan-Lee Pang.

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
package kubeadm

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/scheme"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/certs"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/controlplane"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/etcd"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubeconfig"
	etcdutil "k8s.io/kubernetes/cmd/kubeadm/app/util/etcd"
	kubeadmv1beta1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/types/v1beta1"
)

type Configuration struct {
	InitConfiguration    kubeadmv1beta1.InitConfiguration
	ClusterConfiguration kubeadmv1beta1.ClusterConfiguration
}

func Defaults() *Configuration {
	config := Configuration{}
	scheme.Scheme.Default(&config.InitConfiguration)
	scheme.Scheme.Default(&config.ClusterConfiguration)
	return &config
}

func DefaultCluster() *kubeadmv1beta1.ClusterConfiguration {
	cc := kubeadmv1beta1.ClusterConfiguration{}
	scheme.Scheme.Default(&cc)
	return &cc
}

func DefaultInit() *kubeadmv1beta1.InitConfiguration {
	ic := kubeadmv1beta1.InitConfiguration{}
	scheme.Scheme.Default(&ic)
	return &ic
}

func (c *Configuration) GenerateSecrets() ([]corev1.Secret, error) {
	tmpdir, err := ioutil.TempDir("", "kubernetes")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpdir)
	certsDir := path.Join(tmpdir, "pki")
	kubeconfigDir := path.Join(tmpdir, "kubeconfig")

	initConfig := kubeadmapi.InitConfiguration{}
	scheme.Scheme.Convert(&c.InitConfiguration, &initConfig, nil)
	scheme.Scheme.Convert(&c.ClusterConfiguration, &initConfig.ClusterConfiguration, nil)
	initConfig.ClusterConfiguration.CertificatesDir = certsDir

	if err := certs.CreatePKIAssets(&initConfig); err != nil {
		return nil, err
	}
	if err := kubeconfig.CreateJoinControlPlaneKubeConfigFiles(kubeconfigDir, &initConfig); err != nil {
		return nil, err
	}

	secrets := []corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "k8s-certs",
			},
			Data: map[string][]byte{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "etcd-certs",
			},
			Data: map[string][]byte{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kubeconfig",
			},
			Data: map[string][]byte{},
		},
	}

	files, err := ioutil.ReadDir(certsDir)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if !file.IsDir() {
			contents, err := ioutil.ReadFile(fmt.Sprintf("%s/%s", certsDir, file.Name()))
			if err != nil {
				return nil, err
			}
			secrets[0].Data[file.Name()] = contents
		}
		if file.IsDir() && file.Name() == "etcd" {
			etcdFiles, err := ioutil.ReadDir(fmt.Sprintf("%s/etcd", certsDir))
			if err != nil {
				return nil, err
			}
			for _, etcdFile := range etcdFiles {
				contents, err := ioutil.ReadFile(fmt.Sprintf("%s/etcd/%s", certsDir, etcdFile.Name()))
				if err != nil {
					return nil, err
				}
				secrets[1].Data[etcdFile.Name()] = contents
			}
		}
	}

	kubeconfigs, err := ioutil.ReadDir(kubeconfigDir)
	if err != nil {
		return nil, err
	}

	for _, file := range kubeconfigs {
		if file.IsDir() {
			continue
		}
		contents, err := ioutil.ReadFile(fmt.Sprintf("%s/%s", kubeconfigDir, file.Name()))
		if err != nil {
			return nil, err
		}
		secrets[2].Data[file.Name()] = contents
	}
	return secrets, nil
}

func (c *Configuration) ControlPlanePodSpec() *appsv1.Deployment {
	initConfig := kubeadmapi.InitConfiguration{}
	scheme.Scheme.Convert(&c.InitConfiguration, &initConfig, nil)
	scheme.Scheme.Convert(&c.ClusterConfiguration, &initConfig.ClusterConfiguration, nil)
	pods := controlplane.GetStaticPodSpecs(&initConfig.ClusterConfiguration, &initConfig.LocalAPIEndpoint)

	combined := corev1.Pod{
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{
					Name: "k8s-certs",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: "k8s-certs",
						},
					},
				},
				{
					Name: "etcd-certs",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: "etcd-certs",
						},
					},
				},
				{
					Name: "etcd-data",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: "flexvolume-dir",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: "kubeconfig",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: "kubeconfig",
						},
					},
				},
				{
					Name: "tunnel-client",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: "tunnel-client",
						},
					},
				},
				{
					Name: "ca-certs",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/etc/ssl/certs",
							Type: hostPathTypePtr(corev1.HostPathDirectoryOrCreate),
						},
					},
				},
			},
		},
	}

	for _, pod := range pods {
		for n := range pod.Spec.Containers {
			if pod.Spec.Containers[n].LivenessProbe != nil && pod.Spec.Containers[n].LivenessProbe.HTTPGet != nil {
				// Substitute 127.0.0.1 with empty string so liveness will use pod ip instead.
				pod.Spec.Containers[n].LivenessProbe.HTTPGet.Host = ""
			}
			for i := range pod.Spec.Containers[n].VolumeMounts {
				if pod.Spec.Containers[n].VolumeMounts[i].Name == "kubeconfig" {
					pod.Spec.Containers[n].VolumeMounts[i].MountPath = "/etc/kubernetes"
				}
			}
			for i := range pod.Spec.Containers[n].Command {
				pod.Spec.Containers[n].Command[i] = strings.ReplaceAll(pod.Spec.Containers[n].Command[i], "--bind-address=127.0.0.1", "--bind-address=0.0.0.0")
			}
			if pod.Spec.Containers[n].Name == "kube-apiserver" {
				pod.Spec.Containers[n].VolumeMounts = append(pod.Spec.Containers[n].VolumeMounts, corev1.VolumeMount{Name: "etcd-certs", MountPath: "/etc/kubernetes/pki/etcd"})
			}
		}
		combined.Spec.Containers = append(combined.Spec.Containers, pod.Spec.Containers...)
	}

	etcdPod := etcd.GetEtcdPodSpec(&initConfig.ClusterConfiguration, &initConfig.LocalAPIEndpoint, "controlplane", []etcdutil.Member{})
	for n := range etcdPod.Spec.Containers {
		if etcdPod.Spec.Containers[n].LivenessProbe != nil && etcdPod.Spec.Containers[n].LivenessProbe.HTTPGet != nil {
			// Substitute 127.0.0.1 with empty string so liveness will use etcdPod ip instead.
			etcdPod.Spec.Containers[n].LivenessProbe.HTTPGet.Host = ""
		}
		for i := range etcdPod.Spec.Containers[n].Command {
			etcdPod.Spec.Containers[n].Command[i] = strings.ReplaceAll(etcdPod.Spec.Containers[n].Command[i], "--listen-client-urls=https://127.0.0.1:2379,https://172.17.0.10:2379", "--listen-client-urls=https://0.0.0.0:2379")
			etcdPod.Spec.Containers[n].Command[i] = strings.ReplaceAll(etcdPod.Spec.Containers[n].Command[i], "--listen-metrics-urls=http://127.0.0.1:2381", "--listen-metrics-urls=http://0.0.0.0:2381")
			etcdPod.Spec.Containers[n].Command[i] = strings.ReplaceAll(etcdPod.Spec.Containers[n].Command[i], "--listen-peer-urls=https://172.17.0.10:2380", "--listen-peer-urls=https://0.0.0.0:2380")
		}
	}
	combined.Spec.Containers = append(combined.Spec.Containers, etcdPod.Spec.Containers...)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "controlplane",
			Labels: map[string]string{
				"app": "controlplane",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "controlplane",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "controlplane",
					Labels: map[string]string{
						"app": "controlplane",
					},
				},
				Spec: combined.Spec,
			},
		},
	}
}

func ControlPlaneServiceSpec() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "controlplane",
			Labels: map[string]string{
				"app": "controlplane",
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Selector: map[string]string{
				"app": "controlplane",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "kube-apiserver",
					Protocol:   corev1.ProtocolTCP,
					Port:       6443,
					TargetPort: intstr.FromInt(6443),
				},
			},
		},
	}
}

func hostPathTypePtr(h corev1.HostPathType) *corev1.HostPathType {
	return &h
}

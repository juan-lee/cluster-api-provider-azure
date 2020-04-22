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
package etcd

import (
	etcdv1beta2 "github.com/coreos/etcd-operator/pkg/apis/etcd/v1beta2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func EtcdOperatorDeploymentSpec() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "etcd-operator",
			Labels: map[string]string{
				"app": "etcd-operator",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "etcd-operator",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "etcd-operator",
					Labels: map[string]string{
						"app": "etcd-operator",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "etcd-operator",
							Image:   "quay.io/coreos/etcd-operator:v0.9.4",
							Command: []string{"etcd-operator"},
							Env: []corev1.EnvVar{
								{
									Name:      "MY_POD_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
								},
								{
									Name:      "MY_POD_NAME",
									ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}},
								},
							},
						},
					},
				},
			},
		},
	}
}

func EtcdCluster() *etcdv1beta2.EtcdCluster {
	return &etcdv1beta2.EtcdCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "etcd-cluster",
			Labels: map[string]string{
				"app": "etcd-cluster",
			},
		},
		Spec: etcdv1beta2.ClusterSpec{
			Size: 3,
			// https://github.com/kubernetes/kubernetes/issues/81508#issuecomment-590646553
			Version: "3.1.19",
			TLS: &etcdv1beta2.TLSPolicy{
				Static: &etcdv1beta2.StaticTLS{
					Member: &etcdv1beta2.MemberSecret{
						PeerSecret:   "etcd-peer-certs",
						ServerSecret: "etcd-server-certs",
					},
					OperatorSecret: "etcd-operator-certs",
				},
			},
		},
	}
}

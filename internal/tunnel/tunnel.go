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
package tunnel

import (
	"errors"
	"fmt"

	"golang.zx2c4.com/wireguard/wgcfg"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
)

const (
	clientConf = `[Interface]
Address = 172.17.0.10/32
SaveConfig = false
PrivateKey = %s

[Peer]
PublicKey = %s
Endpoint = tunnel-server:51820
AllowedIPs = 172.17.0.0/24,172.16.0.0/16,192.168.0.0/16,10.1.0.0/16
PersistentKeepalive = 30`

	serverConf = `[Interface]
Address = 172.17.0.1
SaveConfig = false
PostUp = iptables -A FORWARD -i wg0 -j ACCEPT; iptables -A FORWARD -o wg0 -j ACCEPT; iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT; iptables -D FORWARD -o wg0 -j ACCEPT; iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE
ListenPort = 51820
PrivateKey = %s

[Peer]
PublicKey = %s
AllowedIPs = 172.17.0.10/32

[Peer]
PublicKey = %s
AllowedIPs = 172.17.0.12/32,172.16.0.0/16,192.168.0.0/16,10.1.0.0/16`

	clusterConf = `[Interface]
Address = 172.17.0.12/32
SaveConfig = false
PrivateKey = %s

[Peer]
PublicKey = %s
Endpoint = %s:51820
AllowedIPs = 172.17.0.0/24
PersistentKeepalive = 30`
)

func Secrets(serviceIP string) ([]corev1.Secret, error) {
	if serviceIP == "" {
		return nil, errors.New("Missing serviceIP")
	}
	client, err := wgcfg.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	server, err := wgcfg.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	cluster, err := wgcfg.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	secrets := []corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tunnel-client",
			},
			Data: map[string][]byte{
				"privatekey": []byte(client.String()),
				"publickey":  []byte(client.Public().Base64()),
				"wg0.conf":   []byte(fmt.Sprintf(clientConf, client.String(), server.Public().Base64())),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tunnel-server",
			},
			Data: map[string][]byte{
				"privatekey": []byte(server.String()),
				"publickey":  []byte(server.Public().Base64()),
				"wg0.conf":   []byte(fmt.Sprintf(serverConf, server.String(), client.Public().Base64(), cluster.Public().Base64())),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tunnel-cluster",
			},
			Data: map[string][]byte{
				"privatekey": []byte(cluster.String()),
				"publickey":  []byte(cluster.Public().Base64()),
				"wg0.conf":   []byte(fmt.Sprintf(clusterConf, cluster.String(), server.Public().Base64(), serviceIP)),
			},
		},
	}
	return secrets, nil
}

func ClientPodSpec() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tunnel-client",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Image:           "juanlee/wireguard:latest",
					ImagePullPolicy: corev1.PullAlways,
					Name:            "tunnel-client",
					SecurityContext: &corev1.SecurityContext{
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{
								"NET_ADMIN",
								"SYS_MODULE",
							},
						},
						Privileged: pointer.BoolPtr(true),
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "tunnel-client",
							MountPath: "/etc/wireguard",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "tunnel-client",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: "tunnel-client",
						},
					},
				},
			},
		},
	}
}

func ServerPodSpec() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tunnel-server",
			Labels: map[string]string{
				"app": "tunnel-server",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "tunnel-server",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "tunnel-server",
					Labels: map[string]string{
						"app": "tunnel-server",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Image:           "juanlee/wireguard:latest",
							ImagePullPolicy: corev1.PullAlways,
							Name:            "tunnel-server",
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{
										"NET_ADMIN",
										"SYS_MODULE",
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "tunnel-server",
									MountPath: "/etc/wireguard",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "tunnel-server",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "tunnel-server",
								},
							},
						},
					},
				},
			},
		},
	}
}

func ClusterPodSpec() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tunnel-client",
			Namespace: "kube-system",
			Labels: map[string]string{
				"app": "tunnel-client",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "tunnel-client",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "tunnel-client",
					Labels: map[string]string{
						"app": "tunnel-client",
					},
				},
				Spec: corev1.PodSpec{
					HostNetwork: true,
					Containers: []corev1.Container{
						{
							Image:           "juanlee/wireguard:latest",
							ImagePullPolicy: corev1.PullAlways,
							Name:            "tunnel-client",
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{
										"NET_ADMIN",
										"SYS_MODULE",
									},
								},
								Privileged: pointer.BoolPtr(true),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "tunnel-client",
									MountPath: "/etc/wireguard",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "tunnel-client",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "tunnel-client",
								},
							},
						},
					},
				},
			},
		},
	}
}

func ServerServiceSpec() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tunnel-server",
			Labels: map[string]string{
				"app": "tunnel-server",
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Selector: map[string]string{
				"app": "tunnel-server",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "wireguard",
					Protocol:   corev1.ProtocolUDP,
					Port:       51820,
					TargetPort: intstr.FromInt(51820),
				},
			},
		},
	}
}

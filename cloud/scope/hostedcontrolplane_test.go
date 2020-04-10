/*
Copyright 2019 The Kubernetes Authors.

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

package scope

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/v1beta2"
	"sigs.k8s.io/cluster-api-provider-azure/internal/kubeadm"

	. "github.com/onsi/gomega"
)

func TestParseKubeAdmConfig(t *testing.T) {
	g := NewWithT(t)

	var sampleYaml = `
    ## template: jinja
    #cloud-config
    
    write_files:
    -   path: /tmp/kubeadm.yaml
        owner: root:root
        permissions: '0640'
        content: |
          ---
          apiVersion: kubeadm.k8s.io/v1beta1
          clusterName: keiyoshi-capz-cluster
          controlPlaneEndpoint: keiyoshi-capz-cluster-8d7fdbd.southcentralus.cloudapp.azure.com:6443
          kind: ClusterConfiguration
          kubernetesVersion: v1.17.4
          networking:
            podSubnet: 192.168.0.0/16
          ---
          apiVersion: kubeadm.k8s.io/v1beta1
          kind: InitConfiguration
          nodeRegistration:
            kubeletExtraArgs:
              cloud-config: /etc/kubernetes/azure.json
              cloud-provider: azure
    
    runcmd:
      - 'kubeadm init --config /tmp/kubeadm.yaml '
    `
	t.Run("parseKubeadmConfigTest", func(t *testing.T) {
		config, _ := parseKubeAdmConfig(sampleYaml)
		kubeletExtraArgs := make(map[string]string)
		kubeletExtraArgs["cloud-config"] = "/etc/kubernetes/azure.json"
		kubeletExtraArgs["cloud-provider"] = "azure"

		res := &kubeadm.Configuration{
			InitConfiguration: v1beta2.InitConfiguration{
				TypeMeta: metav1.TypeMeta{
					Kind:       "InitConfiguration",
					APIVersion: "kubeadm.k8s.io/v1beta1",
				},
				NodeRegistration: v1beta2.NodeRegistrationOptions{
					KubeletExtraArgs: kubeletExtraArgs,
				},
			},
			ClusterConfiguration: v1beta2.ClusterConfiguration{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ClusterConfiguration",
					APIVersion: "kubeadm.k8s.io/v1beta1",
				},
				ClusterName:          "keiyoshi-capz-cluster",
				ControlPlaneEndpoint: "keiyoshi-capz-cluster-8d7fdbd.southcentralus.cloudapp.azure.com:6443",
				KubernetesVersion:    "v1.17.4",
				Networking: v1beta2.Networking{
					PodSubnet: "192.168.0.0/16",
				},
			},
		}
		g.Expect(config).To(Equal(res))
	})
}

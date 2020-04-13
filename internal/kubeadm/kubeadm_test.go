/*
Copyright 2020 The Kubernetes Authors.

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

package kubeadm_test

import (
	"testing"

	"sigs.k8s.io/cluster-api-provider-azure/internal/kubeadm"
)

func TestDefaults(t *testing.T) {
	config := kubeadm.Configuration{}
	config.InitConfiguration.LocalAPIEndpoint.AdvertiseAddress = "172.17.0.10"
	config.InitConfiguration.NodeRegistration.Name = "controlplane"
	config.ClusterConfiguration.KubernetesVersion = "v1.18.0"
	config.ClusterConfiguration.Networking.ServiceSubnet = "172.18.0.0/12"
	podSpec := config.ControlPlaneDeploymentSpec()
	if podSpec == nil {
		t.Error("Failed to generate the ControlPlaneDeploymentSpec")
	}

	secrets, err := config.GenerateSecrets()
	if err != nil {
		t.Error(err)
	}

	if len(secrets) != 3 {
		t.Error("Wrong number of secrets")
	}
}

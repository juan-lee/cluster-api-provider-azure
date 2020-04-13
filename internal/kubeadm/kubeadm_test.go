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
	capikubeadmv1beta1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/types/v1beta1"
)

func TestDefaults(t *testing.T) {
	config := kubeadm.Configuration{}
	config.InitConfiguration.LocalAPIEndpoint.AdvertiseAddress = "172.17.0.10"
	config.InitConfiguration.NodeRegistration.Name = "controlplane"
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

func TestConvert(t *testing.T) {
	initConfig := capikubeadmv1beta1.InitConfiguration{}
	clusterConfig := capikubeadmv1beta1.ClusterConfiguration{}
	initConfig.LocalAPIEndpoint.AdvertiseAddress = "172.17.0.10"
	initConfig.NodeRegistration.Name = "controlplane"

	config, err := kubeadm.New(&initConfig, &clusterConfig)
	if err != nil {
		t.Error(err)
	}

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

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

package controllers

import (
	"fmt"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/scope"
	"sigs.k8s.io/cluster-api-provider-azure/internal/kubeadm"
	"sigs.k8s.io/cluster-api-provider-azure/internal/tunnel"
	capierrors "sigs.k8s.io/cluster-api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// azureHCPService are list of services required by cluster actuator, easy to create a fake
type azureHCPService struct {
	hcpScope      *scope.HostedControlPlaneScope
	clusterScope  *scope.ClusterScope
	kubeadmConfig *kubeadm.Configuration
}

// newAzureHCPService populates all the services based on input scope
func newAzureHCPService(hcpScope *scope.HostedControlPlaneScope, clusterScope *scope.ClusterScope) *azureHCPService {
	return &azureHCPService{
		hcpScope:      hcpScope,
		clusterScope:  clusterScope,
		kubeadmConfig: initializeKubeadmConfig(hcpScope),
	}
}

// initializeKubeadmConfig parses the kubeadm yaml generated by the bootstrap provider
func initializeKubeadmConfig(hcpScope *scope.HostedControlPlaneScope) *kubeadm.Configuration {
	config, err := hcpScope.GetKubeAdmConfig()
	if err != nil {
		hcpScope.Error(err, "Failed to get kubeadm config")
		return nil
	}
	config.InitConfiguration.LocalAPIEndpoint.AdvertiseAddress = "172.17.0.10"
	config.InitConfiguration.NodeRegistration.Name = "controlplane"
	config.ClusterConfiguration.KubernetesVersion = "v1.18.0"
	config.ClusterConfiguration.Networking.ServiceSubnet = "172.18.0.0/12"
	return config
}

// Delete reconciles all the services in pre determined order
func (s *azureHCPService) Delete() error {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "controlplane",
			Labels: map[string]string{
				"app": "controlplane",
			},
		},
	}
	deployment.Namespace = s.hcpScope.Namespace()
	if err := s.hcpScope.Client().Delete(s.clusterScope.Context, deployment); err != nil {
		return err
	}
	return nil
}

func (s *azureHCPService) ReconcileControlPlane() error {
	ctx := s.clusterScope.Context
	desired := s.kubeadmConfig.ControlPlaneDeploymentSpec()
	desired.Namespace = s.hcpScope.Namespace()
	desired.Spec.Template.Spec.Containers = append(desired.Spec.Template.Spec.Containers, tunnel.ClientPodSpec().Spec.Containers...)
	existing := appsv1.Deployment{}
	if err := s.hcpScope.Client().Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info("Control Plane pod not found, creating", "name", desired.Name)

			// TODO(jpang): set owner ref
			if err := s.hcpScope.Client().Create(ctx, desired); err != nil {
				return fmt.Errorf("Create control plane pod failed: %w", err)
			}
			return nil
		}
		return fmt.Errorf("Get control plane pod failed: %w", err)
	}

	// klog.Info("Control Plane pod found, updating", "name", desired.Name)
	// if err := s.hcpScope.Client().Update(ctx, desired); err != nil {
	// 	return fmt.Errorf("Update control plane pod failed: %w", err)
	// }

	pods := corev1.PodList{}
	listOptions := client.MatchingLabels(map[string]string{
		"app": "controlplane",
	})
	if err := s.hcpScope.Client().List(ctx, &pods, listOptions); err != nil {
		return fmt.Errorf("List control plane pod failed: %w", err)
	}
	// hack: assuming it's a singleton for now
	if len(pods.Items) == 0 {
		s.hcpScope.Info("HCP pod is being created")
		return nil
	}
	pod := pods.Items[0]
	switch pod.Status.Phase {
	case corev1.PodRunning:
		s.hcpScope.Info("HCP pod is running", "instance-id", pod.GetName())
		s.hcpScope.SetReady()
	case corev1.PodPending:
		s.hcpScope.Info("HCP pod is pending", "instance-id", pod.GetName())
	case corev1.PodFailed:
	case corev1.PodUnknown:
		s.hcpScope.SetFailureReason(capierrors.UpdateMachineError)
		s.hcpScope.SetFailureMessage(errors.New("Pod has gotten into a failued or unknown state"))
	}

	return nil
}

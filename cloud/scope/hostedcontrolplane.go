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

package scope

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/klogr"
	"k8s.io/utils/pointer"
	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	"sigs.k8s.io/cluster-api-provider-azure/internal/kubeadm"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controllers/noderefutil"
	capierrors "sigs.k8s.io/cluster-api/errors"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HostedControlPlaneScopeParams defines the input parameters used to create a new HostedControlPlaneScope.
type HostedControlPlaneScopeParams struct {
	AzureClients
	Client                  client.Client
	Logger                  logr.Logger
	Cluster                 *clusterv1.Cluster
	Machine                 *clusterv1.Machine
	AzureCluster            *infrav1.AzureCluster
	AzureHostedControlPlane *infrav1.AzureHostedControlPlane
}

// NewHostedControlPlaneScope creates a new HostedControlPlaneScope from the supplied parameters.
// This is meant to be called for each reconcile iteration.
func NewHostedControlPlaneScope(params HostedControlPlaneScopeParams) (*HostedControlPlaneScope, error) {
	if params.Client == nil {
		return nil, errors.New("client is required when creating a HostedControlPlaneScope")
	}
	if params.Machine == nil {
		return nil, errors.New("machine is required when creating a HostedControlPlaneScope")
	}
	if params.Cluster == nil {
		return nil, errors.New("cluster is required when creating a HostedControlPlaneScope")
	}
	if params.AzureCluster == nil {
		return nil, errors.New("azure cluster is required when creating a HostedControlPlaneScope")
	}
	if params.AzureHostedControlPlane == nil {
		return nil, errors.New("azure machine is required when creating a HostedControlPlaneScope")
	}

	if params.Logger == nil {
		params.Logger = klogr.New()
	}

	helper, err := patch.NewHelper(params.AzureHostedControlPlane, params.Client)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init patch helper")
	}
	return &HostedControlPlaneScope{
		client:                  params.Client,
		Cluster:                 params.Cluster,
		Machine:                 params.Machine,
		AzureCluster:            params.AzureCluster,
		AzureHostedControlPlane: params.AzureHostedControlPlane,
		Logger:                  params.Logger,
		patchHelper:             helper,
	}, nil
}

// HostedControlPlaneScope defines a scope defined around a machine and its cluster.
type HostedControlPlaneScope struct {
	logr.Logger
	client      client.Client
	patchHelper *patch.Helper

	Cluster                 *clusterv1.Cluster
	Machine                 *clusterv1.Machine
	AzureCluster            *infrav1.AzureCluster
	AzureHostedControlPlane *infrav1.AzureHostedControlPlane
}

// Location returns the AzureMachine location.
func (m *HostedControlPlaneScope) Location() string {
	return m.AzureCluster.Spec.Location
}

// Name returns the AzureMachine name.
func (m *HostedControlPlaneScope) Name() string {
	return m.AzureHostedControlPlane.Name
}

// Namespace returns the namespace name.
func (m *HostedControlPlaneScope) Namespace() string {
	return m.AzureHostedControlPlane.Namespace
}

// IsControlPlane returns true if the machine is a control plane.
func (m *HostedControlPlaneScope) IsControlPlane() bool {
	return util.IsControlPlaneMachine(m.Machine)
}

// Role returns the machine role from the labels.
func (m *HostedControlPlaneScope) Role() string {
	if util.IsControlPlaneMachine(m.Machine) {
		return infrav1.ControlPlane
	}
	return infrav1.Node
}

// GetVMID returns the AzureMachine instance id by parsing Spec.ProviderID.
func (m *HostedControlPlaneScope) GetVMID() *string {
	parsed, err := noderefutil.NewProviderID(m.GetProviderID())
	if err != nil {
		return nil
	}
	return pointer.StringPtr(parsed.ID())
}

// GetProviderID returns the AzureMachine providerID from the spec.
func (m *HostedControlPlaneScope) GetProviderID() string {
	if m.AzureHostedControlPlane.Spec.ProviderID != nil {
		return *m.AzureHostedControlPlane.Spec.ProviderID
	}
	return ""
}

// SetProviderID sets the AzureMachine providerID in spec.
func (m *HostedControlPlaneScope) SetProviderID(v string) {
	m.AzureHostedControlPlane.Spec.ProviderID = pointer.StringPtr(v)
}

// GetVMState returns the AzureMachine VM state.
func (m *HostedControlPlaneScope) GetVMState() *infrav1.VMState {
	return m.AzureHostedControlPlane.Status.VMState
}

// SetVMState sets the AzureMachine VM state.
func (m *HostedControlPlaneScope) SetVMState(v infrav1.VMState) {
	m.AzureHostedControlPlane.Status.VMState = &v
}

// SetReady sets the AzureMachine Ready Status
func (m *HostedControlPlaneScope) SetReady() {
	m.AzureHostedControlPlane.Status.Ready = true
}

// SetFailureMessage sets the AzureMachine status failure message.
func (m *HostedControlPlaneScope) SetFailureMessage(v error) {
	m.AzureHostedControlPlane.Status.FailureMessage = pointer.StringPtr(v.Error())
}

// SetFailureReason sets the AzureMachine status failure reason.
func (m *HostedControlPlaneScope) SetFailureReason(v capierrors.MachineStatusError) {
	m.AzureHostedControlPlane.Status.FailureReason = &v
}

// SetAnnotation sets a key value annotation on the AzureMachine.
func (m *HostedControlPlaneScope) SetAnnotation(key, value string) {
	if m.AzureHostedControlPlane.Annotations == nil {
		m.AzureHostedControlPlane.Annotations = map[string]string{}
	}
	m.AzureHostedControlPlane.Annotations[key] = value
}

// SetAddresses sets the Azure address status.
func (m *HostedControlPlaneScope) SetAddresses(addrs []corev1.NodeAddress) {
	m.AzureHostedControlPlane.Status.Addresses = addrs
}

// Client returns the runtime client
func (m *HostedControlPlaneScope) Client() client.Client {
	return m.client
}

// PatchObject persists the machine spec and status.
func (m *HostedControlPlaneScope) PatchObject() error {
	return m.patchHelper.Patch(context.TODO(), m.AzureHostedControlPlane)
}

// Close the MachineScope by updating the machine spec, machine status.
func (m *HostedControlPlaneScope) Close() error {
	return m.patchHelper.Patch(context.TODO(), m.AzureHostedControlPlane)
}

// AdditionalTags merges AdditionalTags from the scope's AzureCluster and AzureHostedControlPlane. If the same key is present in both,
// the value from AzureHostedControlPlane takes precedence.
func (m *HostedControlPlaneScope) AdditionalTags() infrav1.Tags {
	tags := make(infrav1.Tags)

	// Start with the cluster-wide tags...
	tags.Merge(m.AzureCluster.Spec.AdditionalTags)
	// ... and merge in the Machine's
	tags.Merge(m.AzureHostedControlPlane.Spec.AdditionalTags)

	return tags
}

// GetKubeAdmConfig returns the kubeadm config from the secret in the Machine's bootstrap.dataSecretName.
func (m *HostedControlPlaneScope) GetKubeAdmConfig() (*kubeadm.Configuration, error) {
	if m.Machine.Spec.Bootstrap.DataSecretName == nil {
		return nil, errors.New("error retrieving bootstrap data: linked Machine's bootstrap.dataSecretName is nil")
	}
	var config *bootstrapv1.KubeadmConfig
	key := types.NamespacedName{Namespace: m.Namespace(), Name: m.Machine.Spec.Bootstrap.ConfigRef.Name}
	if err := m.client.Get(context.TODO(), key, config); err != nil {
		return nil, errors.Wrapf(err, "failed to retrieve kubeadm bootstrap config for AzureHostedControlPlane %s/%s", m.Namespace(), m.Name())
	}

	kubeadmConfig := kubeadm.Defaults()
	kubeadmConfig.InitConfiguration = *config.Spec.InitConfiguration
	kubeadmConfig.ClusterConfiguration = *config.Spec.ClusterConfiguration
	return kubeadmConfig, nil
}

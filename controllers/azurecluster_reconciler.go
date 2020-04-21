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
	"hash/fnv"
	"strings"

	etcdv1beta2 "github.com/coreos/etcd-operator/pkg/apis/etcd/v1beta2"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	azure "sigs.k8s.io/cluster-api-provider-azure/cloud"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/scope"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/services/groups"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/services/internalloadbalancers"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/services/publicips"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/services/publicloadbalancers"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/services/routetables"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/services/securitygroups"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/services/subnets"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/services/virtualnetworks"
	"sigs.k8s.io/cluster-api-provider-azure/internal/etcd"
	"sigs.k8s.io/cluster-api-provider-azure/internal/kubeadm"
	"sigs.k8s.io/cluster-api-provider-azure/internal/tunnel"
)

type ClusterReconciler interface {
	Reconcile() error
	Delete() error
}

// azureClusterReconciler are list of services required by cluster controller
type azureClusterReconciler struct {
	scope            *scope.ClusterScope
	groupsSvc        azure.Service
	vnetSvc          azure.Service
	securityGroupSvc azure.Service
	routeTableSvc    azure.Service
	subnetsSvc       azure.Service
	internalLBSvc    azure.Service
	publicIPSvc      azure.Service
	publicLBSvc      azure.Service
}

// hcpClusterReconciler are list of services required by cluster controller
type hcpClusterReconciler struct {
	scope            *scope.ClusterScope
	groupsSvc        azure.Service
	vnetSvc          azure.Service
	securityGroupSvc azure.Service
	routeTableSvc    azure.Service
	subnetsSvc       azure.Service
}

func newClusterReconciler(scope *scope.ClusterScope) ClusterReconciler {
	if scope.AzureCluster.Spec.IsHostedControlPlane {
		return newHCPClusterReconciler(scope)
	}
	return newAzureClusterReconciler(scope)
}

// newAzureClusterReconciler populates all the services based on input scope
func newAzureClusterReconciler(scope *scope.ClusterScope) *azureClusterReconciler {
	return &azureClusterReconciler{
		scope:            scope,
		groupsSvc:        groups.NewService(scope),
		vnetSvc:          virtualnetworks.NewService(scope),
		securityGroupSvc: securitygroups.NewService(scope),
		routeTableSvc:    routetables.NewService(scope),
		subnetsSvc:       subnets.NewService(scope),
		internalLBSvc:    internalloadbalancers.NewService(scope),
		publicIPSvc:      publicips.NewService(scope),
		publicLBSvc:      publicloadbalancers.NewService(scope),
	}
}

// hcpClusterReconciler populates all the services based on input scope
func newHCPClusterReconciler(scope *scope.ClusterScope) *hcpClusterReconciler {
	return &hcpClusterReconciler{
		scope:            scope,
		groupsSvc:        groups.NewService(scope),
		vnetSvc:          virtualnetworks.NewService(scope),
		securityGroupSvc: securitygroups.NewService(scope),
		routeTableSvc:    routetables.NewService(scope),
		subnetsSvc:       subnets.NewService(scope),
	}
}

// Reconcile reconciles all the services in pre determined order
func (r *azureClusterReconciler) Reconcile() error {
	klog.V(2).Infof("reconciling cluster %s", r.scope.Name())
	r.createOrUpdateNetworkAPIServerIP()

	if err := r.groupsSvc.Reconcile(r.scope.Context, nil); err != nil {
		return errors.Wrapf(err, "failed to reconcile resource group for cluster %s", r.scope.Name())
	}

	if r.scope.Vnet().ResourceGroup == "" {
		r.scope.Vnet().ResourceGroup = r.scope.ResourceGroup()
	}
	if r.scope.Vnet().Name == "" {
		r.scope.Vnet().Name = azure.GenerateVnetName(r.scope.Name())
	}
	if r.scope.Vnet().CidrBlock == "" {
		r.scope.Vnet().CidrBlock = azure.DefaultVnetCIDR
	}

	if len(r.scope.Subnets()) == 0 {
		r.scope.AzureCluster.Spec.NetworkSpec.Subnets = infrav1.Subnets{&infrav1.SubnetSpec{}, &infrav1.SubnetSpec{}}
	}

	vnetSpec := &virtualnetworks.Spec{
		ResourceGroup: r.scope.Vnet().ResourceGroup,
		Name:          r.scope.Vnet().Name,
		CIDR:          r.scope.Vnet().CidrBlock,
	}
	if err := r.vnetSvc.Reconcile(r.scope.Context, vnetSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile virtual network for cluster %s", r.scope.Name())
	}
	sgName := azure.GenerateControlPlaneSecurityGroupName(r.scope.Name())
	if r.scope.ControlPlaneSubnet() != nil && r.scope.ControlPlaneSubnet().SecurityGroup.Name != "" {
		sgName = r.scope.ControlPlaneSubnet().SecurityGroup.Name
	}
	sgSpec := &securitygroups.Spec{
		Name:           sgName,
		IsControlPlane: true,
	}
	if err := r.securityGroupSvc.Reconcile(r.scope.Context, sgSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile control plane network security group for cluster %s", r.scope.Name())
	}

	sgName = azure.GenerateNodeSecurityGroupName(r.scope.Name())
	if r.scope.NodeSubnet() != nil && r.scope.NodeSubnet().SecurityGroup.Name != "" {
		sgName = r.scope.NodeSubnet().SecurityGroup.Name
	}
	sgSpec = &securitygroups.Spec{
		Name:           sgName,
		IsControlPlane: false,
	}
	if err := r.securityGroupSvc.Reconcile(r.scope.Context, sgSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile node network security group for cluster %s", r.scope.Name())
	}

	rtSpec := &routetables.Spec{
		Name: azure.GenerateNodeRouteTableName(r.scope.Name()),
	}
	if err := r.routeTableSvc.Reconcile(r.scope.Context, rtSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile node route table for cluster %s", r.scope.Name())
	}

	cpSubnet := r.scope.ControlPlaneSubnet()
	if cpSubnet == nil {
		cpSubnet = &infrav1.SubnetSpec{}
	}
	if cpSubnet.Role == "" {
		cpSubnet.Role = infrav1.SubnetControlPlane
	}
	if cpSubnet.Name == "" {
		cpSubnet.Name = azure.GenerateControlPlaneSubnetName(r.scope.Name())
	}
	if cpSubnet.CidrBlock == "" {
		cpSubnet.CidrBlock = azure.DefaultControlPlaneSubnetCIDR
	}
	if cpSubnet.SecurityGroup.Name == "" {
		cpSubnet.SecurityGroup.Name = azure.GenerateControlPlaneSecurityGroupName(r.scope.Name())
	}

	subnetSpec := &subnets.Spec{
		Name:                cpSubnet.Name,
		CIDR:                cpSubnet.CidrBlock,
		VnetName:            r.scope.Vnet().Name,
		SecurityGroupName:   cpSubnet.SecurityGroup.Name,
		RouteTableName:      azure.GenerateNodeRouteTableName(r.scope.Name()),
		Role:                cpSubnet.Role,
		InternalLBIPAddress: cpSubnet.InternalLBIPAddress,
	}
	if err := r.subnetsSvc.Reconcile(r.scope.Context, subnetSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile control plane subnet for cluster %s", r.scope.Name())
	}

	nodeSubnet := r.scope.NodeSubnet()
	if nodeSubnet == nil {
		nodeSubnet = &infrav1.SubnetSpec{}
	}
	if nodeSubnet.Role == "" {
		nodeSubnet.Role = infrav1.SubnetNode
	}
	if nodeSubnet.Name == "" {
		nodeSubnet.Name = azure.GenerateNodeSubnetName(r.scope.Name())
	}
	if nodeSubnet.CidrBlock == "" {
		nodeSubnet.CidrBlock = azure.DefaultNodeSubnetCIDR
	}
	if nodeSubnet.SecurityGroup.Name == "" {
		nodeSubnet.SecurityGroup.Name = azure.GenerateNodeSecurityGroupName(r.scope.Name())
	}

	subnetSpec = &subnets.Spec{
		Name:              nodeSubnet.Name,
		CIDR:              nodeSubnet.CidrBlock,
		VnetName:          r.scope.Vnet().Name,
		SecurityGroupName: nodeSubnet.SecurityGroup.Name,
		RouteTableName:    azure.GenerateNodeRouteTableName(r.scope.Name()),
		Role:              nodeSubnet.Role,
	}
	if err := r.subnetsSvc.Reconcile(r.scope.Context, subnetSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile node subnet for cluster %s", r.scope.Name())
	}

	internalLBSpec := &internalloadbalancers.Spec{
		Name:       azure.GenerateInternalLBName(r.scope.Name()),
		SubnetName: r.scope.ControlPlaneSubnet().Name,
		SubnetCidr: r.scope.ControlPlaneSubnet().CidrBlock,
		VnetName:   r.scope.Vnet().Name,
		IPAddress:  r.scope.ControlPlaneSubnet().InternalLBIPAddress,
	}
	if err := r.internalLBSvc.Reconcile(r.scope.Context, internalLBSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile control plane internal load balancer for cluster %s", r.scope.Name())
	}

	publicIPSpec := &publicips.Spec{
		Name: r.scope.Network().APIServerIP.Name,
	}
	if err := r.publicIPSvc.Reconcile(r.scope.Context, publicIPSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile control plane public ip for cluster %s", r.scope.Name())
	}

	publicLBSpec := &publicloadbalancers.Spec{
		Name:         azure.GeneratePublicLBName(r.scope.Name()),
		PublicIPName: r.scope.Network().APIServerIP.Name,
	}
	if err := r.publicLBSvc.Reconcile(r.scope.Context, publicLBSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile control plane public load balancer for cluster %s", r.scope.Name())
	}

	return nil
}

// Delete reconciles all the services in pre determined order
func (r *azureClusterReconciler) Delete() error {
	if r.scope.Vnet().ResourceGroup == "" {
		r.scope.Vnet().ResourceGroup = r.scope.ResourceGroup()
	}
	if r.scope.Vnet().Name == "" {
		r.scope.Vnet().Name = azure.GenerateVnetName(r.scope.Name())
	}

	if err := r.deleteLB(); err != nil {
		return errors.Wrap(err, "failed to delete load balancer")
	}

	if err := r.deleteSubnets(); err != nil {
		return errors.Wrap(err, "failed to delete subnets")
	}

	rtSpec := &routetables.Spec{
		Name: azure.GenerateNodeRouteTableName(r.scope.Name()),
	}
	if err := r.routeTableSvc.Delete(r.scope.Context, rtSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete route table %s for cluster %s", azure.GenerateNodeRouteTableName(r.scope.Name()), r.scope.Name())
		}
	}

	if err := r.deleteNSG(); err != nil {
		return errors.Wrap(err, "failed to delete network security group")
	}

	vnetSpec := &virtualnetworks.Spec{
		ResourceGroup: r.scope.Vnet().ResourceGroup,
		Name:          r.scope.Vnet().Name,
	}
	if err := r.vnetSvc.Delete(r.scope.Context, vnetSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete virtual network %s for cluster %s", r.scope.Vnet().Name, r.scope.Name())
		}
	}

	if err := r.groupsSvc.Delete(r.scope.Context, nil); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete resource group for cluster %s", r.scope.Name())
		}
	}

	return nil
}

func (r *azureClusterReconciler) deleteLB() error {
	publicLBSpec := &publicloadbalancers.Spec{
		Name: azure.GeneratePublicLBName(r.scope.Name()),
	}
	if err := r.publicLBSvc.Delete(r.scope.Context, publicLBSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete lb %s for cluster %s", azure.GeneratePublicLBName(r.scope.Name()), r.scope.Name())
		}
	}
	publicIPSpec := &publicips.Spec{
		Name: r.scope.Network().APIServerIP.Name,
	}
	if err := r.publicIPSvc.Delete(r.scope.Context, publicIPSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete public ip %s for cluster %s", r.scope.Network().APIServerIP.Name, r.scope.Name())
		}
	}

	internalLBSpec := &internalloadbalancers.Spec{
		Name: azure.GenerateInternalLBName(r.scope.Name()),
	}
	if err := r.internalLBSvc.Delete(r.scope.Context, internalLBSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to internal load balancer %s for cluster %s", azure.GenerateInternalLBName(r.scope.Name()), r.scope.Name())
		}
	}

	return nil
}

func (r *azureClusterReconciler) deleteSubnets() error {
	for _, s := range r.scope.Subnets() {
		subnetSpec := &subnets.Spec{
			Name:     s.Name,
			VnetName: r.scope.Vnet().Name,
		}
		if err := r.subnetsSvc.Delete(r.scope.Context, subnetSpec); err != nil {
			if !azure.ResourceNotFound(err) {
				return errors.Wrapf(err, "failed to delete %s subnet for cluster %s", s.Name, r.scope.Name())
			}
		}
	}
	return nil
}

func (r *azureClusterReconciler) deleteNSG() error {
	sgSpec := &securitygroups.Spec{
		Name: azure.GenerateNodeSecurityGroupName(r.scope.Name()),
	}
	if err := r.securityGroupSvc.Delete(r.scope.Context, sgSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete security group %s for cluster %s", azure.GenerateNodeSecurityGroupName(r.scope.Name()), r.scope.Name())
		}
	}
	sgSpec = &securitygroups.Spec{
		Name: azure.GenerateControlPlaneSecurityGroupName(r.scope.Name()),
	}
	if err := r.securityGroupSvc.Delete(r.scope.Context, sgSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete security group %s for cluster %s", azure.GenerateControlPlaneSecurityGroupName(r.scope.Name()), r.scope.Name())
		}
	}

	return nil
}

// CreateOrUpdateNetworkAPIServerIP creates or updates public ip name and dns name
func (r *azureClusterReconciler) createOrUpdateNetworkAPIServerIP() {
	if r.scope.Network().APIServerIP.Name == "" {
		h := fnv.New32a()
		h.Write([]byte(fmt.Sprintf("%s/%s/%s", r.scope.SubscriptionID, r.scope.ResourceGroup(), r.scope.Name())))
		r.scope.Network().APIServerIP.Name = azure.GeneratePublicIPName(r.scope.Name(), fmt.Sprintf("%x", h.Sum32()))
	}

	r.scope.Network().APIServerIP.DNSName = azure.GenerateFQDN(r.scope.Network().APIServerIP.Name, r.scope.Location())
}

// Reconcile reconciles all the services in pre determined order
func (r *hcpClusterReconciler) Reconcile() error {
	klog.V(2).Infof("reconciling cluster %s", r.scope.Name())

	if err := r.groupsSvc.Reconcile(r.scope.Context, nil); err != nil {
		return errors.Wrapf(err, "failed to reconcile resource group for cluster %s", r.scope.Name())
	}

	if r.scope.Vnet().ResourceGroup == "" {
		r.scope.Vnet().ResourceGroup = r.scope.ResourceGroup()
	}
	if r.scope.Vnet().Name == "" {
		r.scope.Vnet().Name = azure.GenerateVnetName(r.scope.Name())
	}
	if r.scope.Vnet().CidrBlock == "" {
		r.scope.Vnet().CidrBlock = azure.DefaultVnetCIDR
	}

	if len(r.scope.Subnets()) == 0 {
		r.scope.AzureCluster.Spec.NetworkSpec.Subnets = infrav1.Subnets{&infrav1.SubnetSpec{}, &infrav1.SubnetSpec{}}
	}

	vnetSpec := &virtualnetworks.Spec{
		ResourceGroup: r.scope.Vnet().ResourceGroup,
		Name:          r.scope.Vnet().Name,
		CIDR:          r.scope.Vnet().CidrBlock,
	}
	if err := r.vnetSvc.Reconcile(r.scope.Context, vnetSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile virtual network for cluster %s", r.scope.Name())
	}
	sgName := azure.GenerateControlPlaneSecurityGroupName(r.scope.Name())
	if r.scope.ControlPlaneSubnet() != nil && r.scope.ControlPlaneSubnet().SecurityGroup.Name != "" {
		sgName = r.scope.ControlPlaneSubnet().SecurityGroup.Name
	}
	sgSpec := &securitygroups.Spec{
		Name:           sgName,
		IsControlPlane: true,
	}
	if err := r.securityGroupSvc.Reconcile(r.scope.Context, sgSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile control plane network security group for cluster %s", r.scope.Name())
	}

	sgName = azure.GenerateNodeSecurityGroupName(r.scope.Name())
	if r.scope.NodeSubnet() != nil && r.scope.NodeSubnet().SecurityGroup.Name != "" {
		sgName = r.scope.NodeSubnet().SecurityGroup.Name
	}
	sgSpec = &securitygroups.Spec{
		Name:           sgName,
		IsControlPlane: false,
	}
	if err := r.securityGroupSvc.Reconcile(r.scope.Context, sgSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile node network security group for cluster %s", r.scope.Name())
	}

	rtSpec := &routetables.Spec{
		Name: azure.GenerateNodeRouteTableName(r.scope.Name()),
	}
	if err := r.routeTableSvc.Reconcile(r.scope.Context, rtSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile node route table for cluster %s", r.scope.Name())
	}

	cpSubnet := r.scope.ControlPlaneSubnet()
	if cpSubnet == nil {
		cpSubnet = &infrav1.SubnetSpec{}
	}
	if cpSubnet.Role == "" {
		cpSubnet.Role = infrav1.SubnetControlPlane
	}
	if cpSubnet.Name == "" {
		cpSubnet.Name = azure.GenerateControlPlaneSubnetName(r.scope.Name())
	}
	if cpSubnet.CidrBlock == "" {
		cpSubnet.CidrBlock = azure.DefaultControlPlaneSubnetCIDR
	}
	if cpSubnet.SecurityGroup.Name == "" {
		cpSubnet.SecurityGroup.Name = azure.GenerateControlPlaneSecurityGroupName(r.scope.Name())
	}

	subnetSpec := &subnets.Spec{
		Name:                cpSubnet.Name,
		CIDR:                cpSubnet.CidrBlock,
		VnetName:            r.scope.Vnet().Name,
		SecurityGroupName:   cpSubnet.SecurityGroup.Name,
		RouteTableName:      azure.GenerateNodeRouteTableName(r.scope.Name()),
		Role:                cpSubnet.Role,
		InternalLBIPAddress: cpSubnet.InternalLBIPAddress,
	}
	if err := r.subnetsSvc.Reconcile(r.scope.Context, subnetSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile control plane subnet for cluster %s", r.scope.Name())
	}

	nodeSubnet := r.scope.NodeSubnet()
	if nodeSubnet == nil {
		nodeSubnet = &infrav1.SubnetSpec{}
	}
	if nodeSubnet.Role == "" {
		nodeSubnet.Role = infrav1.SubnetNode
	}
	if nodeSubnet.Name == "" {
		nodeSubnet.Name = azure.GenerateNodeSubnetName(r.scope.Name())
	}
	if nodeSubnet.CidrBlock == "" {
		nodeSubnet.CidrBlock = azure.DefaultNodeSubnetCIDR
	}
	if nodeSubnet.SecurityGroup.Name == "" {
		nodeSubnet.SecurityGroup.Name = azure.GenerateNodeSecurityGroupName(r.scope.Name())
	}

	subnetSpec = &subnets.Spec{
		Name:              nodeSubnet.Name,
		CIDR:              nodeSubnet.CidrBlock,
		VnetName:          r.scope.Vnet().Name,
		SecurityGroupName: nodeSubnet.SecurityGroup.Name,
		RouteTableName:    azure.GenerateNodeRouteTableName(r.scope.Name()),
		Role:              nodeSubnet.Role,
	}
	if err := r.subnetsSvc.Reconcile(r.scope.Context, subnetSpec); err != nil {
		return errors.Wrapf(err, "failed to reconcile node subnet for cluster %s", r.scope.Name())
	}

	if err := r.reconcileTunnel(); err != nil {
		return errors.Wrap(err, "failed to reconcile tunnel")
	}
	if err := r.reconcileControlPlaneService(); err != nil {
		return errors.Wrap(err, "failed to reconcile hosted control plane load balancer service")
	}
	if err := r.reconcileControlPlaneSecrets(); err != nil {
		return errors.Wrap(err, "failed to reconcile hosted control plane secrets")
	}
	if err := r.reconcileEtcd(); err != nil {
		return errors.Wrap(err, "failed to reconcile etcd cluster")
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileTunnel() error {
	if err := r.reconcileTunnelService(); err != nil {
		return err
	}
	if err := r.reconcileTunnelSecrets(); err != nil {
		return err
	}
	if err := r.reconcileTunnelServer(); err != nil {
		return err
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileTunnelSecrets() error {
	secrets, err := tunnel.Secrets(r.scope.AzureCluster.Status.TunnelIP)
	if err != nil {
		return err
	}

	klog.Info("Reconciling tunnel secrets")
	for _, secret := range secrets {
		secret.Namespace = r.scope.Namespace()
		existing := corev1.Secret{}
		if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, &existing); err != nil {
			if apierrors.IsNotFound(err) {
				klog.Infof("Secret not found, creating - %s", secret.Name)

				// TODO(jpang): set owner ref
				if err := r.scope.Client().Create(r.scope.Context, &secret); err != nil {
					return err
				}
				continue
			}
			return err
		}
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileTunnelService() error {
	desired := tunnel.ServerServiceSpec()
	desired.Namespace = r.scope.Namespace()
	existing := corev1.Service{}
	if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info("Tunnel Server service not found, creating")

			// TODO(jpang): set owner ref
			if err := r.scope.Client().Create(r.scope.Context, desired); err != nil {
				return err
			}
			return nil
		}
		return err
	}
	if len(existing.Status.LoadBalancer.Ingress) > 0 {
		r.scope.AzureCluster.Status.TunnelIP = existing.Status.LoadBalancer.Ingress[0].IP
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileTunnelServer() error {
	desired := tunnel.ServerPodSpec()
	desired.Namespace = r.scope.Namespace()
	existing := appsv1.Deployment{}
	if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info("Tunnel Server pod not found, creating")

			// TODO(jpang): set owner ref
			if err := r.scope.Client().Create(r.scope.Context, desired); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	// klog.Infof("Tunnel Server pod found, updating - %s", desired.Name)
	// if err := r.scope.Client().Update(r.scope.Context, desired); err != nil {
	// 	return err
	// }
	return nil
}

// HACK(jpang): doesn't belong in this controller
func (r *hcpClusterReconciler) reconcileControlPlaneSecrets() error {
	if r.scope.Network().APIServerIP.DNSName == "" {
		klog.Info("ControlPlaneEndpoint not set, skipping secret generation... :)")
		return nil
	}

	config := kubeadm.Configuration{}
	config.InitConfiguration.NodeRegistration.Name = "controlplane"
	config.ClusterConfiguration.ControlPlaneEndpoint = r.scope.Network().APIServerIP.DNSName
	config.InitConfiguration.LocalAPIEndpoint.AdvertiseAddress = strings.Split(config.ClusterConfiguration.ControlPlaneEndpoint, ":")[0]
	if r.scope.Cluster.Spec.ClusterNetwork.Services != nil {
		config.ClusterConfiguration.Networking.ServiceSubnet = r.scope.Cluster.Spec.ClusterNetwork.Services.CIDRBlocks[0]
	}

	// hack: not checking <hcp-name>-ca, etc.
	certsExist, err := r.checkIfCertsExist()
	if err != nil {
		return err
	}
	if certsExist {
		klog.Info("Returning early as control plane cert secrets already exist")
		return nil
	}

	// Generate certs and create secrets
	klog.Info("Reconciling tunnel secrets")
	secrets, err := config.GenerateSecrets()
	if err != nil {
		return err
	}

	for _, secret := range secrets {
		secret.Namespace = r.scope.Namespace()
		existing := corev1.Secret{}
		if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, &existing); err != nil {
			if apierrors.IsNotFound(err) {
				klog.Infof("Secret not found, creating - %s", secret.Name)

				// TODO(jpang): set owner ref
				if err := r.scope.Client().Create(r.scope.Context, &secret); err != nil {
					return err
				}
				continue
			}
			return err
		}
	}
	for _, secret := range secrets {
		if err := r.reconcileKubernetesCerts(&secret); err != nil {
			return err
		}
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileKubernetesCerts(cert *corev1.Secret) error {
	if cert.Name == "k8s-certs" {
		if err := r.reconcileCertificate("ca", "ca", cert); err != nil {
			return err
		}
		if err := r.reconcileCertificate("sa", "sa", cert); err != nil {
			return err
		}
		if err := r.reconcileCertificate("front-proxy-ca", "proxy", cert); err != nil {
			return err
		}
	}
	if cert.Name == "etcd-certs" {
		if err := r.reconcileCertificate("ca", "etcd", cert); err != nil {
			return err
		}
	}
	if cert.Name == "kubeconfig" {
		if err := r.reconcileKubeconfig(cert); err != nil {
			return err
		}
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileCertificate(source, dest string, cert *corev1.Secret) error {
	desired := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", r.scope.Cluster.Name, dest),
			Namespace: r.scope.Namespace(),
		},
		Data: map[string][]byte{
			"tls.key": cert.Data[fmt.Sprintf("%s.key", source)],
			"tls.crt": cert.Data[fmt.Sprintf("%s.crt", source)],
		},
	}
	existing := corev1.Secret{}
	if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Secret not found, creating - %s", desired.Name)

			// TODO(jpang): set owner ref
			if err := r.scope.Client().Create(r.scope.Context, &desired); err != nil {
				return err
			}
			return nil
		}
		return err
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileKubeconfig(cert *corev1.Secret) error {
	desired := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-kubeconfig", r.scope.Cluster.Name),
			Namespace: r.scope.Namespace(),
		},
		Data: map[string][]byte{
			"value": cert.Data["admin.conf"],
		},
	}
	existing := corev1.Secret{}
	if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Secret not found, creating - %s", desired.Name)

			// TODO(jpang): set owner ref
			if err := r.scope.Client().Create(r.scope.Context, &desired); err != nil {
				return err
			}
			return nil
		}
		return err
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileEtcd() error {
	// setup RBAC for etcd operator (added role & rolebinding to rbac directory)
	// create etcd operator deployment
	if err := r.reconcileEtcdOperator(); err != nil {
		return err
	}
	// create etcd cluster
	if err := r.reconcileEtcdCluster(); err != nil {
		return err
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileEtcdOperator() error {
	desired := etcd.EtcdOperatorDeploymentSpec()
	desired.Namespace = r.scope.Namespace()
	existing := appsv1.Deployment{}
	if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Etcd Operator deployment not found, creating: %s", desired.Name)

			if err := r.scope.Client().Create(r.scope.Context, desired); err != nil {
				return err
			}
			return nil
		}
		return err
	}
	return nil
}

func (r *hcpClusterReconciler) reconcileEtcdCluster() error {
	desired := etcd.EtcdCluster()
	desired.Namespace = r.scope.Namespace()
	existing := etcdv1beta2.EtcdCluster{}
	if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Etcd Cluster not found, creating: %s", desired.Name)

			if err := r.scope.Client().Create(r.scope.Context, desired); err != nil {
				return err
			}
			return nil
		}
		return err
	}
	return nil
}

func (r *hcpClusterReconciler) checkIfCertsExist() (bool, error) {
	// check if certs have already been generated
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
				Name: "etcd-peer-certs",
			},
			Data: map[string][]byte{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "etcd-server-certs",
			},
			Data: map[string][]byte{},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "etcd-operator-certs",
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
	for _, secret := range secrets {
		secret.Namespace = r.scope.Namespace()
		existing := corev1.Secret{}
		if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, &existing); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func (r *hcpClusterReconciler) reconcileControlPlaneService() error {
	desired := kubeadm.ControlPlaneServiceSpec()
	desired.Namespace = r.scope.Namespace()
	existing := corev1.Service{}
	if err := r.scope.Client().Get(r.scope.Context, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info("Control Plane service not found, creating")

			// TODO(jpang): set owner ref
			if err := r.scope.Client().Create(r.scope.Context, desired); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	if len(existing.Status.LoadBalancer.Ingress) > 0 {
		r.scope.Network().APIServerIP.DNSName = existing.Status.LoadBalancer.Ingress[0].IP
	}
	return nil
}

// Delete reconciles all the services in pre determined order
func (r *hcpClusterReconciler) Delete() error {
	if r.scope.Vnet().ResourceGroup == "" {
		r.scope.Vnet().ResourceGroup = r.scope.ResourceGroup()
	}
	if r.scope.Vnet().Name == "" {
		r.scope.Vnet().Name = azure.GenerateVnetName(r.scope.Name())
	}

	if err := r.deleteSubnets(); err != nil {
		return errors.Wrap(err, "failed to delete subnets")
	}

	rtSpec := &routetables.Spec{
		Name: azure.GenerateNodeRouteTableName(r.scope.Name()),
	}
	if err := r.routeTableSvc.Delete(r.scope.Context, rtSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete route table %s for cluster %s", azure.GenerateNodeRouteTableName(r.scope.Name()), r.scope.Name())
		}
	}

	if err := r.deleteNSG(); err != nil {
		return errors.Wrap(err, "failed to delete network security group")
	}

	vnetSpec := &virtualnetworks.Spec{
		ResourceGroup: r.scope.Vnet().ResourceGroup,
		Name:          r.scope.Vnet().Name,
	}
	if err := r.vnetSvc.Delete(r.scope.Context, vnetSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete virtual network %s for cluster %s", r.scope.Vnet().Name, r.scope.Name())
		}
	}

	if err := r.groupsSvc.Delete(r.scope.Context, nil); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete resource group for cluster %s", r.scope.Name())
		}
	}

	return nil
}

func (r *hcpClusterReconciler) deleteSubnets() error {
	for _, s := range r.scope.Subnets() {
		subnetSpec := &subnets.Spec{
			Name:     s.Name,
			VnetName: r.scope.Vnet().Name,
		}
		if err := r.subnetsSvc.Delete(r.scope.Context, subnetSpec); err != nil {
			if !azure.ResourceNotFound(err) {
				return errors.Wrapf(err, "failed to delete %s subnet for cluster %s", s.Name, r.scope.Name())
			}
		}
	}
	return nil
}

func (r *hcpClusterReconciler) deleteNSG() error {
	sgSpec := &securitygroups.Spec{
		Name: azure.GenerateNodeSecurityGroupName(r.scope.Name()),
	}
	if err := r.securityGroupSvc.Delete(r.scope.Context, sgSpec); err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to delete security group %s for cluster %s", azure.GenerateNodeSecurityGroupName(r.scope.Name()), r.scope.Name())
		}
	}
	return nil
}

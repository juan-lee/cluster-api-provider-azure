/*
Copyright The Kubernetes Authors.
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
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/scope"
)

// AzureHostedControlPlaneReconciler reconciles a AzureHostedControlPlane object
type AzureHostedControlPlaneReconciler struct {
	client.Client
	Log      logr.Logger
	Recorder record.EventRecorder
}

func (r *AzureHostedControlPlaneReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&infrav1.AzureHostedControlPlane{}).
		Watches(
			&source.Kind{Type: &clusterv1.Machine{}},
			&handler.EnqueueRequestsFromMapFunc{
				ToRequests: util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("AzureHostedControlPlane")),
			},
		).
		Watches(
			&source.Kind{Type: &infrav1.AzureCluster{}},
			&handler.EnqueueRequestsFromMapFunc{ToRequests: handler.ToRequestsFunc(r.AzureClusterToAzureMachines)},
		).
		Complete(r)
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=azurehostedcontrolplanes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=azurehostedcontrolplanes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=kubeadmconfigs,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets;,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods;,verbs=get;list;watch

func (r *AzureHostedControlPlaneReconciler) Reconcile(req ctrl.Request) (_ ctrl.Result, reterr error) {
	ctx := context.Background()
	logger := r.Log.WithValues("namespace", req.Namespace, "azurehostedcontrolplane", req.Name)

	// Fetch the AzureHostedControlPlane instance.
	azureHCP := &infrav1.AzureHostedControlPlane{}
	err := r.Get(ctx, req.NamespacedName, azureHCP)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Fetch the Machine.
	machine, err := util.GetOwnerMachine(ctx, r.Client, azureHCP.ObjectMeta)
	if err != nil {
		return reconcile.Result{}, err
	}
	if machine == nil {
		logger.Info("Machine Controller has not yet set OwnerRef")
		return reconcile.Result{}, nil
	}

	logger = logger.WithValues("machine", machine.Name)

	// Fetch the Cluster.
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		logger.Info("Machine is missing cluster label or cluster does not exist")
		return reconcile.Result{}, nil
	}

	logger = logger.WithValues("cluster", cluster.Name)

	azureCluster := &infrav1.AzureCluster{}

	azureClusterName := client.ObjectKey{
		Namespace: azureHCP.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}
	if err := r.Client.Get(ctx, azureClusterName, azureCluster); err != nil {
		logger.Info("AzureCluster is not available yet")
		return reconcile.Result{}, nil
	}

	logger = logger.WithValues("azureCluster", azureCluster.Name)

	// Create the cluster scope
	clusterScope, err := scope.NewClusterScope(scope.ClusterScopeParams{
		Client:       r.Client,
		Logger:       logger,
		Cluster:      cluster,
		AzureCluster: azureCluster,
	})
	if err != nil {
		return reconcile.Result{}, err
	}

	// Create the HCP scope
	hcpScope, err := scope.NewHostedControlPlaneScope(scope.HostedControlPlaneScopeParams{
		Logger:                  logger,
		Client:                  r.Client,
		Cluster:                 cluster,
		Machine:                 machine,
		AzureCluster:            azureCluster,
		AzureHostedControlPlane: azureHCP,
	})
	if err != nil {
		return reconcile.Result{}, errors.Errorf("failed to create scope: %+v", err)
	}

	// Always close the scope when exiting this function so we can persist any AzureHostedControlPlane changes.
	defer func() {
		if err := hcpScope.Close(); err != nil && reterr == nil {
			reterr = err
		}
	}()

	// Handle deleted HCP pods
	if !azureHCP.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(hcpScope, clusterScope)
	}

	// Handle non-deleted HCP pods
	return r.reconcileNormal(ctx, hcpScope, clusterScope)
}

func (r *AzureHostedControlPlaneReconciler) reconcileNormal(ctx context.Context, hcpScope *scope.HostedControlPlaneScope, clusterScope *scope.ClusterScope) (reconcile.Result, error) {
	hcpScope.Info("Reconciling AzureHostedControlPlane")
	// If the AzureMachine is in an error state, return early.
	if hcpScope.AzureHostedControlPlane.Status.FailureReason != nil || hcpScope.AzureHostedControlPlane.Status.FailureMessage != nil {
		hcpScope.Info("Error state detected, skipping reconciliation")
		return reconcile.Result{}, nil
	}

	// If the AzureHostedControlPlane doesn't have our finalizer, add it.
	controllerutil.AddFinalizer(hcpScope.AzureHostedControlPlane, infrav1.MachineFinalizer)
	// Register the finalizer immediately to avoid orphaning Azure resources on delete
	if err := hcpScope.PatchObject(); err != nil {
		return reconcile.Result{}, err
	}

	if !hcpScope.Cluster.Status.InfrastructureReady {
		hcpScope.Info("Cluster infrastructure is not ready yet")
		return reconcile.Result{}, nil
	}

	// Make sure bootstrap data is available and populated.
	if hcpScope.Machine.Spec.Bootstrap.DataSecretName == nil {
		hcpScope.Info("Bootstrap data secret reference is not yet available")
		return reconcile.Result{}, nil
	}

	// Setup kubeadm and necessary resources
	// Assume that necessary secrets are generated in the cluster controller to be shared by CAPI and the hcp pod spec
	// TODO: In the future we want to have our own bootstrap provider to do this instead of duplicating the secrets
	//   and also to have the bootstrap secret in the format we want (we don't need the cloud-init format).
	hcpService := newAzureHCPService(hcpScope, clusterScope)

	if err := hcpService.ReconcileControlPlane(); err != nil {
		return reconcile.Result{}, err
	}

	// Make sure Spec.ProviderID is always set.
	// hcpScope.SetProviderID(fmt.Sprintf("azure:////%s", vm.ID))

	// Proceed to reconcile the AzureMachine state.
	// hcpScope.SetVMState(vm.State)

	// TODO(vincepri): Remove this annotation when clusterctl is no longer relevant.
	hcpScope.SetAnnotation("cluster-api-provider-azure", "true")

	// hcpScope.SetAddresses(vm.Addresses)

	// TODO: Ensure that the tags are correct.
	// err = r.reconcileTags(hcpScope, clusterScope, hcpScope.AdditionalTags())
	// if err != nil {
	// 	return reconcile.Result{}, errors.Errorf("failed to ensure tags: %+v", err)
	// }

	return reconcile.Result{}, nil
}

func (r *AzureHostedControlPlaneReconciler) reconcileDelete(hcpScope *scope.HostedControlPlaneScope, clusterScope *scope.ClusterScope) (_ reconcile.Result, reterr error) {
	hcpScope.Info("Handling deleted AzureHostedControlPlane")

	if err := newAzureHCPService(hcpScope, clusterScope).Delete(); err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "error deleting AzureCluster %s/%s", clusterScope.Namespace(), clusterScope.Name())
	}

	defer func() {
		if reterr == nil {
			// HCP Pod is deleted so remove the finalizer.
			controllerutil.RemoveFinalizer(hcpScope.AzureHostedControlPlane, infrav1.MachineFinalizer)
		}
	}()

	return reconcile.Result{}, nil
}

// AzureClusterToAzureMachine is a handler.ToRequestsFunc to be used to enqueue requests for reconciliation
// of AzureMachines.
func (r *AzureHostedControlPlaneReconciler) AzureClusterToAzureMachines(o handler.MapObject) []ctrl.Request {
	result := []ctrl.Request{}

	c, ok := o.Object.(*infrav1.AzureCluster)
	if !ok {
		r.Log.Error(errors.Errorf("expected a AzureCluster but got a %T", o.Object), "failed to get AzureMachine for AzureCluster")
		return nil
	}
	log := r.Log.WithValues("AzureCluster", c.Name, "Namespace", c.Namespace)

	cluster, err := util.GetOwnerCluster(context.TODO(), r.Client, c.ObjectMeta)
	switch {
	case apierrors.IsNotFound(err) || cluster == nil:
		return result
	case err != nil:
		log.Error(err, "failed to get owning cluster")
		return result
	}

	labels := map[string]string{clusterv1.ClusterLabelName: cluster.Name}
	machineList := &clusterv1.MachineList{}
	if err := r.List(context.TODO(), machineList, client.InNamespace(c.Namespace), client.MatchingLabels(labels)); err != nil {
		log.Error(err, "failed to list Machines")
		return nil
	}
	for _, m := range machineList.Items {
		if m.Spec.InfrastructureRef.Name == "" {
			continue
		}
		name := client.ObjectKey{Namespace: m.Namespace, Name: m.Spec.InfrastructureRef.Name}
		result = append(result, ctrl.Request{NamespacedName: name})
	}

	return result
}

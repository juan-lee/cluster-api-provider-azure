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
	"fmt"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/util"
	kcfg "sigs.k8s.io/cluster-api/util/kubeconfig"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	"sigs.k8s.io/cluster-api-provider-azure/cloud/scope"
	"sigs.k8s.io/cluster-api-provider-azure/internal/kubeadm"
	"sigs.k8s.io/cluster-api-provider-azure/internal/tunnel"
)

// AzureHostedControlPlaneReconciler reconciles a AzureHostedControlPlane object
type AzureHostedControlPlaneReconciler struct {
	client.Client
	Log      logr.Logger
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
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
			&handler.EnqueueRequestsFromMapFunc{ToRequests: handler.ToRequestsFunc(r.AzureClusterToAzureHostedControlPlanes)},
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
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the Machine.
	machine, err := util.GetOwnerMachine(ctx, r.Client, azureHCP.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		logger.Info("Machine Controller has not yet set OwnerRef")
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("machine", machine.Name)

	// Fetch the Cluster.
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		logger.Info("Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("cluster", cluster.Name)

	azureCluster := &infrav1.AzureCluster{}

	azureClusterName := client.ObjectKey{
		Namespace: azureHCP.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}
	if err := r.Client.Get(ctx, azureClusterName, azureCluster); err != nil {
		logger.Info("AzureCluster is not available yet")
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("azureCluster", azureCluster.Name)

	// Create the HCP scope
	hcpScope, err := scope.NewHostedControlPlaneScope(scope.HostedControlPlaneScopeParams{
		Logger:                  logger,
		Client:                  r.Client,
		Cluster:                 cluster,
		Machine:                 machine,
		AzureCluster:            azureCluster,
		AzureHostedControlPlane: azureHCP,
		Scheme:                  r.Scheme,
		Context:                 ctx,
	})
	if err != nil {
		return ctrl.Result{}, errors.Errorf("failed to create scope: %+v", err)
	}

	// Always close the scope when exiting this function so we can persist any AzureHostedControlPlane changes.
	defer func() {
		if err := hcpScope.Close(); err != nil && reterr == nil {
			reterr = err
		}
	}()

	// Handle deleted HCP pods
	if !azureHCP.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, hcpScope)
	}

	// Handle non-deleted HCP pods
	return r.reconcileNormal(ctx, hcpScope)
}

func (r *AzureHostedControlPlaneReconciler) reconcileNormal(ctx context.Context, hcpScope *scope.HostedControlPlaneScope) (ctrl.Result, error) {
	hcpScope.Info("Reconciling AzureHostedControlPlane")
	// If the AzureHostedControlPlane is in an error state, return early.
	if hcpScope.AzureHostedControlPlane.Status.FailureReason != nil || hcpScope.AzureHostedControlPlane.Status.FailureMessage != nil {
		hcpScope.Info("Error state detected, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// If the AzureHostedControlPlane doesn't have our finalizer, add it.
	controllerutil.AddFinalizer(hcpScope.AzureHostedControlPlane, infrav1.AzureHostedControlPlaneFinalizer)
	// Register the finalizer immediately to avoid orphaning Azure resources on delete
	if err := hcpScope.PatchObject(); err != nil {
		return ctrl.Result{}, err
	}

	if !hcpScope.Cluster.Status.InfrastructureReady {
		hcpScope.Info("Cluster infrastructure is not ready yet")
		return ctrl.Result{}, nil
	}

	// Make sure bootstrap data is available and populated.
	if hcpScope.Machine.Spec.Bootstrap.DataSecretName == nil {
		hcpScope.Info("Bootstrap data secret reference is not yet available")
		return ctrl.Result{}, nil
	}

	// Setup kubeadm and necessary resources
	// Assume that necessary secrets are generated in the cluster controller to be shared by CAPI and the hcp pod spec
	// TODO: In the future we want to have our own bootstrap provider to do this instead of duplicating the secrets
	//   and also to have the bootstrap secret in the format we want (we don't need the cloud-init format).
	if result, err := r.reconcileHCP(ctx, hcpScope); err != nil {
		hcpScope.Error(err, "Error in reconcile control plane")
		return result, err
	}

	// TODO: Alter upstream code so we don't need this
	hcpScope.SetProviderID(fmt.Sprintf("azure:////%s", hcpScope.Name()))

	// TODO(vincepri): Remove this annotation when clusterctl is no longer relevant.
	hcpScope.SetAnnotation("cluster-api-provider-azure", "true")

	// TODO: Ensure that the tags are correct.
	// err = r.reconcileTags(hcpScope, clusterScope, hcpScope.AdditionalTags())
	// if err != nil {
	// 	return ctrl.Result{}, errors.Errorf("failed to ensure tags: %+v", err)
	// }

	return ctrl.Result{}, nil
}

func (r *AzureHostedControlPlaneReconciler) reconcileHCP(ctx context.Context, hcpScope *scope.HostedControlPlaneScope) (ctrl.Result, error) {
	if hcpScope.AzureHostedControlPlane.Status.Ready {
		hcpScope.Logger.Info("ControlPlane is ready, skipping reconcile")
		return ctrl.Result{}, nil
	}
	config, err := hcpScope.GetKubeadmConfig()
	if err != nil {
		hcpScope.Error(err, "Failed to get kubeadm config")
		return ctrl.Result{}, err
	}
	if result, err := r.reconcileControlPlaneDeployment(ctx, hcpScope, config); err != nil {
		return result, err
	}
	if result, err := r.reconcilePostControlPlaneInit(ctx, hcpScope, config); err != nil {
		return result, err
	}
	hcpScope.SetReady()
	return ctrl.Result{}, nil
}

func (r *AzureHostedControlPlaneReconciler) reconcileControlPlaneDeployment(ctx context.Context, hcpScope *scope.HostedControlPlaneScope, kubeadmConfig *kubeadm.Configuration) (ctrl.Result, error) {
	desired := kubeadmConfig.ControlPlaneDeploymentSpec()
	desired.Namespace = hcpScope.Namespace()
	desired.Spec.Template.Spec.Containers = append(desired.Spec.Template.Spec.Containers, tunnel.ClientPodSpec().Spec.Containers...)
	existing := appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Control Plane pod not found, creating: %s", desired.Name)

			// TODO(jpang): set owner ref
			if err := r.Create(ctx, desired); err != nil {
				return ctrl.Result{}, fmt.Errorf("Create control plane pod failed: %w", err)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("Get control plane pod failed: %w", err)
	}

	// klog.Info("Control Plane pod found, updating", "name", desired.Name)
	// if err := s.hcpScope.Client().Update(ctx, desired); err != nil {
	// 	return fmt.Errorf("Update control plane pod failed: %w", err)
	// }

	if existing.Status.ReadyReplicas == 0 {
		return ctrl.Result{}, errors.New("Control Plane Pod isn't ready")
	}
	return ctrl.Result{}, nil
}

func (r *AzureHostedControlPlaneReconciler) reconcileTunnelDeployment(ctx context.Context, hcpScope *scope.HostedControlPlaneScope, kubeadmConfig *kubeadm.Configuration) (ctrl.Result, error) {
	remoteClient, err := remote.NewClusterClient(ctx, hcpScope.Client(), util.ObjectKey(hcpScope.Cluster), hcpScope.Scheme())
	if err != nil {
		return ctrl.Result{}, err
	}

	desired := tunnel.ClusterPodSpec()
	existing := appsv1.Deployment{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Tunnel deployment not found, creating: %s", desired.Name)

			// TODO(jpang): set owner ref
			if err := remoteClient.Create(ctx, desired); err != nil {
				return ctrl.Result{}, fmt.Errorf("Create tunnel deployment failed: %w", err)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("Get tunnel deployment failed: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *AzureHostedControlPlaneReconciler) reconcileTunnelSecret(ctx context.Context, hcpScope *scope.HostedControlPlaneScope, kubeadmConfig *kubeadm.Configuration) (ctrl.Result, error) {
	secret := corev1.Secret{}
	err := hcpScope.Client().Get(ctx, types.NamespacedName{Namespace: hcpScope.Namespace(), Name: "tunnel-cluster"}, &secret)
	if err != nil {
		return ctrl.Result{}, err
	}
	remoteClient, err := remote.NewClusterClient(ctx, hcpScope.Client(), util.ObjectKey(hcpScope.Cluster), hcpScope.Scheme())
	if err != nil {
		return ctrl.Result{}, err
	}

	desired := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tunnel-client",
			Namespace: "kube-system",
		},
		Data: secret.Data,
		Type: secret.Type,
	}
	existing := corev1.Secret{}
	if err := remoteClient.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Tunnel secret not found, creating: %s", desired.Name)

			// TODO(jpang): set owner ref
			if err := remoteClient.Create(ctx, &desired); err != nil {
				return ctrl.Result{}, fmt.Errorf("Create tunnel secret failed: %w", err)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("Get tunnel secret failed: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *AzureHostedControlPlaneReconciler) reconcilePostControlPlaneInit(ctx context.Context, hcpScope *scope.HostedControlPlaneScope, kubeadmConfig *kubeadm.Configuration) (ctrl.Result, error) {
	kubeConfig, err := kcfg.FromSecret(ctx, r.Client, util.ObjectKey(hcpScope.Cluster))
	if err != nil {
		return ctrl.Result{}, err
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeConfig)
	if err != nil {
		return ctrl.Result{}, err
	}
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := kubeadm.ProvisionBootstrapToken(kubeClient, kubeConfig); err != nil {
		return ctrl.Result{}, fmt.Errorf("CreateBootstrapConfigMapIfNotExists: %w", err)
	}
	if err := kubeadmConfig.UploadConfig(kubeClient); err != nil {
		return ctrl.Result{}, fmt.Errorf("UploadConfig: %w", err)
	}
	if err := kubeadmConfig.EnsureAddons(kubeClient); err != nil {
		return ctrl.Result{}, fmt.Errorf("EnsureAddons: %w", err)
	}
	if result, err := r.reconcileTunnelSecret(ctx, hcpScope, kubeadmConfig); err != nil {
		return result, fmt.Errorf("reconcileTunnelSecret: %w", err)
	}
	if result, err := r.reconcileTunnelDeployment(ctx, hcpScope, kubeadmConfig); err != nil {
		return result, fmt.Errorf("reconcileTunnelDeployment: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *AzureHostedControlPlaneReconciler) reconcileDelete(ctx context.Context, hcpScope *scope.HostedControlPlaneScope) (ctrl.Result, error) {
	hcpScope.Info("Handling deleted AzureHostedControlPlane")

	defer func() {
		// HCP Pod is deleted so remove the finalizer.
		controllerutil.RemoveFinalizer(hcpScope.AzureHostedControlPlane, infrav1.AzureHostedControlPlaneFinalizer)
	}()

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "controlplane",
			Labels: map[string]string{
				"app": "controlplane",
			},
		},
	}
	deployment.Namespace = hcpScope.Namespace()
	if err := hcpScope.Client().Delete(ctx, deployment); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// AzureClusterToAzureHostedControlPlanes is a handler.ToRequestsFunc to be used to enqueue requests for reconciliation
// of AzureHostedControlPlanes.
func (r *AzureHostedControlPlaneReconciler) AzureClusterToAzureHostedControlPlanes(o handler.MapObject) []ctrl.Request {
	result := []ctrl.Request{}

	c, ok := o.Object.(*infrav1.AzureCluster)
	if !ok {
		r.Log.Error(errors.Errorf("expected a AzureCluster but got a %T", o.Object), "failed to get AzureHostedControlPlane for AzureCluster")
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
		if m.Spec.InfrastructureRef.Name == "" || m.Spec.InfrastructureRef.GroupVersionKind() != infrav1.GroupVersion.WithKind("AzureHostedControlPlane") {
			continue
		}
		name := client.ObjectKey{Namespace: m.Namespace, Name: m.Spec.InfrastructureRef.Name}
		result = append(result, ctrl.Request{NamespacedName: name})
	}
	return result
}

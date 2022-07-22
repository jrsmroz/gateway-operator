//go:build integration_tests
// +build integration_tests

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	operatorv1alpha1 "github.com/kong/gateway-operator/apis/v1alpha1"
	"github.com/kong/gateway-operator/internal/consts"
	gatewayutils "github.com/kong/gateway-operator/internal/utils/gateway"
	k8sutils "github.com/kong/gateway-operator/internal/utils/kubernetes"
)

// mustListDataPlaneDeployments is a helper function for tests that
// conveniently lists all deployments managed by a given dataplane.
func mustListDataPlaneDeployments(t *testing.T, dataplane *operatorv1alpha1.DataPlane) []v1.Deployment {
	deployments, err := k8sutils.ListDeploymentsForOwner(
		ctx,
		mgrClient,
		consts.GatewayOperatorControlledLabel,
		consts.DataPlaneManagedLabelValue,
		dataplane.Namespace,
		dataplane.UID,
	)
	require.NoError(t, err)
	return deployments
}

// mustListControlPlaneDeployments is a helper function for tests that
// conveniently lists all deployments managed by a given controlplane.
func mustListControlPlaneDeployments(t *testing.T, controlplane *operatorv1alpha1.ControlPlane) []v1.Deployment {
	deployments, err := k8sutils.ListDeploymentsForOwner(
		ctx,
		mgrClient,
		consts.GatewayOperatorControlledLabel,
		consts.ControlPlaneManagedLabelValue,
		controlplane.Namespace,
		controlplane.UID,
	)
	require.NoError(t, err)
	return deployments
}

func mustListControlPlanesForGateway(t *testing.T, gateway *gatewayv1alpha2.Gateway) []operatorv1alpha1.ControlPlane {
	controlPlanes, err := gatewayutils.ListControlPlanesForGateway(ctx, mgrClient, gateway)
	require.NoError(t, err)
	return controlPlanes
}

func mustListNetworkPoliciesForGateway(t *testing.T, gateway *gatewayv1alpha2.Gateway) []networkingv1.NetworkPolicy {
	networkPolicies, err := gatewayutils.ListNetworkPoliciesForGateway(ctx, mgrClient, gateway)
	require.NoError(t, err)
	return networkPolicies
}

// mustListServices is a helper function for tests that
// conveniently lists all services managed by a given dataplane.
func mustListDataPlaneServices(t *testing.T, dataplane *operatorv1alpha1.DataPlane) []corev1.Service {
	services, err := k8sutils.ListServicesForOwner(
		ctx,
		mgrClient,
		consts.GatewayOperatorControlledLabel,
		consts.DataPlaneManagedLabelValue,
		dataplane.Namespace,
		dataplane.UID,
	)
	require.NoError(t, err)
	return services
}

func mustListDataPlanesForGateway(t *testing.T, gateway *v1alpha2.Gateway) []operatorv1alpha1.DataPlane {
	dataplanes, err := gatewayutils.ListDataPlanesForGateway(ctx, mgrClient, gateway)
	require.NoError(t, err)
	return dataplanes
}

// mustGetGateway is a helper function for tests that
// conveniently gets a gateway by name.
func mustGetGateway(t *testing.T, gatewayNSN types.NamespacedName) *v1alpha2.Gateway {
	gateways := gatewayClient.GatewayV1alpha2().Gateways(gatewayNSN.Namespace)
	gateway, err := gateways.Get(ctx, gatewayNSN.Name, metav1.GetOptions{})
	require.NoError(t, err)
	return gateway
}

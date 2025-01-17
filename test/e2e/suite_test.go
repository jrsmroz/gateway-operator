//go:build e2e_tests
// +build e2e_tests

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters/addons/loadimage"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters/addons/metallb"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters/types/gke"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters/types/kind"
	"github.com/kong/kubernetes-testing-framework/pkg/environments"
	"github.com/kong/kubernetes-testing-framework/pkg/utils/kubernetes/networking"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kong/gateway-operator/pkg/clientset"
	"github.com/kong/gateway-operator/test/consts"
)

// -----------------------------------------------------------------------------
// Testing Vars - Environment Overrideable
// -----------------------------------------------------------------------------

var (
	existingCluster = os.Getenv("KONG_TEST_CLUSTER")
	imageOverride   = os.Getenv("KONG_TEST_GATEWAY_OPERATOR_IMAGE_OVERRIDE")
	imageLoad       = os.Getenv("KONG_TEST_GATEWAY_OPERATOR_IMAGE_LOAD")
)

// -----------------------------------------------------------------------------
// Testing Vars - path of kustomization directories and files
// -----------------------------------------------------------------------------

var (
	kustomizationDir  = "../../config/default"
	kustomizationFile = kustomizationDir + "/kustomization.yaml"
	// backupKustomizationFile is used to save the original kustomization file if we modified it.
	// iIf the kustomization file is changed multiple times,
	// only the content before the first change should be used as backup to keep the content as same as the origin.
	backupKustomizationFile = ""
)

// -----------------------------------------------------------------------------
// Testing Vars - Testing Environment
// -----------------------------------------------------------------------------

var (
	ctx    context.Context
	cancel context.CancelFunc
	env    environments.Environment

	k8sClient      *kubernetes.Clientset
	operatorClient *clientset.Clientset
	mgrClient      client.Client
)

// -----------------------------------------------------------------------------
// Testing Main
// -----------------------------------------------------------------------------

func TestMain(m *testing.M) {
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	var skipClusterCleanup bool
	fmt.Println("INFO: configuring cluster for testing environment")
	builder := environments.NewBuilder()
	if existingCluster != "" {
		clusterParts := strings.Split(existingCluster, ":")
		if len(clusterParts) != 2 {
			exitOnErr(fmt.Errorf("existing cluster in wrong format (%s): format is <TYPE>:<NAME> (e.g. kind:test-cluster)", existingCluster))
		}
		clusterType, clusterName := clusterParts[0], clusterParts[1]

		fmt.Printf("INFO: using existing %s cluster %s\n", clusterType, clusterName)
		switch clusterType {
		case string(kind.KindClusterType):
			cluster, err := kind.NewFromExisting(clusterName)
			exitOnErr(err)
			builder.WithExistingCluster(cluster)
			builder.WithAddons(metallb.New())
		case string(gke.GKEClusterType):
			cluster, err := gke.NewFromExistingWithEnv(ctx, clusterName)
			exitOnErr(err)
			builder.WithExistingCluster(cluster)
		default:
			exitOnErr(fmt.Errorf("unknown cluster type: %s", clusterType))
		}
	} else {
		fmt.Println("INFO: no existing cluster found, deploying using Kubernetes In Docker (KIND)")
		builder.WithAddons(metallb.New())
	}
	if imageLoad != "" {
		imageLoader, err := loadimage.NewBuilder().WithImage(imageLoad)
		exitOnErr(err)
		fmt.Println("INFO: load image", imageLoad)
		builder.WithAddons(imageLoader.Build())
	}
	var err error
	env, err = builder.Build(ctx)
	exitOnErr(err)

	fmt.Printf("INFO: waiting for cluster %s and all addons to become ready\n", env.Cluster().Name())
	exitOnErr(<-env.WaitForReady(ctx))

	fmt.Println("INFO: initializing Kubernetes API clients")
	k8sClient = env.Cluster().Client()
	operatorClient, err = clientset.NewForConfig(env.Cluster().Config())
	exitOnErr(err)
	mgrClient, err = client.New(env.Cluster().Config(), client.Options{})
	exitOnErr(err)

	fmt.Printf("deploying Gateway APIs CRDs from %s\n", consts.GatewayCRDsKustomizeURL)
	exitOnErr(clusters.KustomizeDeployForCluster(ctx, env.Cluster(), consts.GatewayCRDsKustomizeURL))

	fmt.Println("INFO: creating system namespaces and serviceaccounts")
	exitOnErr(clusters.CreateNamespace(ctx, env.Cluster(), "kong-system"))

	exitOnErr(setOperatorImage())

	fmt.Println("INFO: deploying operator to test cluster via kustomize")
	exitOnErr(clusters.KustomizeDeployForCluster(ctx, env.Cluster(), kustomizationDir))

	fmt.Println("INFO: waiting for operator deployment to complete")
	exitOnErr(waitForOperatorDeployment())

	fmt.Println("INFO: waiting for operator webhook service to be connective")
	exitOnErr(waitForOperatorWebhook())

	fmt.Println("INFO: environment is ready, starting tests")
	code := m.Run()

	if skipClusterCleanup || !(existingCluster == "") {
		fmt.Println("INFO: cleaning up operator manifests")
		exitOnErr(clusters.KustomizeDeleteForCluster(ctx, env.Cluster(), kustomizationDir))
	} else {
		fmt.Println("INFO: cleaning up testing cluster and environment")
		exitOnErr(env.Cleanup(ctx))
	}

	exitOnErr(restoreKustomizationFile())

	os.Exit(code)
}

// -----------------------------------------------------------------------------
// Testing Main - Helper Functions
// -----------------------------------------------------------------------------

func exitOnErr(err error) {
	if err != nil {
		if env != nil {
			env.Cleanup(ctx) //nolint:errcheck
		}
		fmt.Printf("ERROR: %s\n", err.Error())
		os.Exit(1)
	}
}

func waitForOperatorDeployment() error {
	ready := false
	for !ready {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			deployment, err := k8sClient.AppsV1().Deployments("kong-system").Get(ctx, "gateway-operator-controller-manager", metav1.GetOptions{})
			if err != nil {
				return err
			}
			if deployment.Status.AvailableReplicas >= 1 {
				ready = true
			}
		}
	}
	return nil
}

func waitForOperatorWebhook() error {
	webhookServiceNamespace := "kong-system"
	webhookServiceName := "gateway-operator-validating-webhook"
	webhookServicePort := 443
	return networking.WaitForConnectionOnServicePort(ctx, k8sClient, webhookServiceNamespace, webhookServiceName, webhookServicePort, 10*time.Second)
}

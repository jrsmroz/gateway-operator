/*
Copyright 2022 Kong Inc.

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

package manager

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math"
	"math/big"
	"os"
	"path"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	operatorv1alpha1 "github.com/kong/gateway-operator/apis/v1alpha1"
	"github.com/kong/gateway-operator/internal/admission"
	"github.com/kong/gateway-operator/internal/manager/metadata"
	"github.com/kong/gateway-operator/internal/telemetry"
	"github.com/kong/gateway-operator/pkg/vars"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

const (
	defaultWebhookCertDir = "/tmp/k8s-webhook-server/serving-certs"
	caCertFilename        = "ca.crt"
	tlsCertFilename       = "tls.crt"
	tlsKeyFilename        = "tls.key"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(operatorv1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayv1alpha2.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

type Config struct {
	MetricsAddr              string
	ProbeAddr                string
	WebhookCertDir           string
	WebhookPort              int
	LeaderElection           bool
	DevelopmentMode          bool
	Out                      *os.File
	NewClientFunc            cluster.NewClientFunc
	ControllerName           string
	AnonymousReports         bool
	APIServerPath            string
	KubeconfigPath           string
	ClusterCASecretName      string
	ClusterCASecretNamespace string
	LoggerOpts               zap.Options

	GatewayControllerEnabled      bool
	ControlPlaneControllerEnabled bool
	DataPlaneControllerEnabled    bool
}

func DefaultConfig() Config {
	return Config{
		MetricsAddr:         ":8080",
		ProbeAddr:           ":8081",
		WebhookPort:         9443,
		DevelopmentMode:     false,
		LeaderElection:      true,
		ClusterCASecretName: "kong-operator-ca",
		// TODO: Extract this into a named const and use it in all the placed where
		// "kong-system" is used verbatim: https://github.com/Kong/gateway-operator/pull/149.
		ClusterCASecretNamespace: "kong-system",
		LoggerOpts:               zap.Options{},

		GatewayControllerEnabled:      true,
		ControlPlaneControllerEnabled: true,
		DataPlaneControllerEnabled:    true,
	}
}

func Run(cfg Config) error {
	if cfg.ControllerName != "" {
		setupLog.Info(fmt.Sprintf("custom controller name provided: %s", cfg.ControllerName))
		vars.ControllerName = cfg.ControllerName
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&cfg.LoggerOpts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     cfg.MetricsAddr,
		Port:                   cfg.WebhookPort,
		HealthProbeBindAddress: cfg.ProbeAddr,
		LeaderElection:         cfg.LeaderElection,
		LeaderElectionID:       "a7feedc84.konghq.com",
		NewClient:              cfg.NewClientFunc,
		// We need to read Deployments and secrets directly from the API server as there is
		// an indeterministic state in which the controller creates a deployment (or a secret)
		// for the controlplane (or dataplane) and returns with success. In this scenario there
		// is already another request in the queue that triggers a new reconciliation loop;
		// when the controller checks whether there are already deployments, no deployment is
		// found because the cache has not been updated yet. In the following reconicilation
		// loop, an error occurs because we have two deployments.
		// TODO: https://github.com/Kong/gateway-operator/issues/182
		ClientDisableCacheFor: []client.Object{
			&appsv1.Deployment{},
			&corev1.Secret{},
		},
	})
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	caMgr := &caManager{
		client:          mgr.GetClient(),
		secretName:      cfg.ClusterCASecretName,
		secretNamespace: cfg.ClusterCASecretNamespace,
	}
	err = mgr.Add(caMgr)
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	// load controllers
	controllers := setupControllers(mgr, &cfg)
	for _, c := range controllers {
		if err := c.MaybeSetupWithManager(mgr); err != nil {
			return fmt.Errorf("unable to create controller %q: %w", c.Name(), err)
		}
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	runWebhookServer(mgr, cfg)

	// Enable anonnymous reporting when configured but not for development builds
	// to reduce the noise.
	if cfg.AnonymousReports && !cfg.DevelopmentMode {
		stopAnonymousReports, err := setupAnonymousReports(cfg)
		if err != nil {
			setupLog.Error(err, "failed setting up anonymous reports")
		} else {
			defer stopAnonymousReports()
		}
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("problem running manager: %w", err)
	}

	return nil
}

func runWebhookServer(mgr manager.Manager, cfg Config) {
	webhookCertDir := cfg.WebhookCertDir
	if webhookCertDir == "" {
		webhookCertDir = defaultWebhookCertDir
	}
	tlsCertPath := path.Join(webhookCertDir, tlsCertFilename)
	tlsKeyPath := path.Join(webhookCertDir, tlsKeyFilename)
	caCertPath := path.Join(webhookCertDir, caCertFilename)

	startWebhook := true
	if _, err := os.Stat(caCertPath); err != nil {
		setupLog.Info("CA certificate file does not exist, do not start webhook", "path", caCertPath)
		return
	}
	if _, err := os.Stat(tlsCertPath); err != nil {
		setupLog.Info("TLS certificate file does not exist, do not start webhook", "path", tlsCertPath)
		return
	}
	if _, err := os.Stat(tlsKeyPath); err != nil {
		setupLog.Info("TLS key file does not exist, do not start webhook", "path", tlsKeyPath)
		return
	}

	if startWebhook {
		hookServer := admission.NewWebhookServerFromManager(mgr, ctrl.Log)
		hookServer.CertDir = webhookCertDir
		// add readyz check for checking connection to webhook server
		// to make the controller to be marked as ready after webhook started.
		err := mgr.AddReadyzCheck("readyz", mgr.GetWebhookServer().StartedChecker())
		if err != nil {
			setupLog.Error(err, "failed to add readiness probe for webhook")
		}

		setupLog.Info("start webhook server", "listen_address", fmt.Sprintf("%s:%d", hookServer.Host, hookServer.Port))
	}
}

type caManager struct {
	client          client.Client
	secretName      string
	secretNamespace string
}

func (m *caManager) Start(ctx context.Context) error {
	if m.secretName == "" {
		return fmt.Errorf("cannot use an empty secret name when creating a CA secret")
	}
	if m.secretNamespace == "" {
		return fmt.Errorf("cannot use an empty secret namespace when creating a CA secret")
	}
	return m.maybeCreateCACertificate(ctx)
}

func (m *caManager) maybeCreateCACertificate(ctx context.Context) error {
	// TODO https://github.com/Kong/gateway-operator/issues/108 this also needs to check if the CA is expired and
	// managed, and needs to reissue it (and all issued certificates) if so
	ca := &corev1.Secret{}
	ctx, cancel := context.WithTimeout(ctx, time.Second*30)
	defer cancel()
	err := m.client.Get(ctx, client.ObjectKey{Namespace: m.secretNamespace, Name: m.secretName}, ca)
	if k8serrors.IsNotFound(err) {
		serial, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
		if err != nil {
			return err
		}
		setupLog.Info(fmt.Sprintf("no CA certificate Secret %s found, generating CA certificate", m.secretName))
		template := x509.Certificate{
			Subject: pkix.Name{
				CommonName:   "Kong Gateway Operator CA",
				Organization: []string{"Kong, Inc."},
				Country:      []string{"US"},
			},
			SerialNumber:          serial,
			SignatureAlgorithm:    x509.ECDSAWithSHA256,
			NotBefore:             time.Now(),
			NotAfter:              time.Now().Add(time.Second * 315400000),
			KeyUsage:              x509.KeyUsageCertSign + x509.KeyUsageKeyEncipherment + x509.KeyUsageDigitalSignature,
			BasicConstraintsValid: true,
			IsCA:                  true,
		}

		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		privDer, err := x509.MarshalECPrivateKey(priv)
		if err != nil {
			return err
		}

		der, err := x509.CreateCertificate(rand.Reader, &template, &template, priv.Public(), priv)
		if err != nil {
			return err
		}

		signedSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: m.secretNamespace,
				Name:      m.secretName,
			},
			Type: corev1.SecretTypeTLS,
			StringData: map[string]string{
				"tls.crt": string(pem.EncodeToMemory(&pem.Block{
					Type:  "CERTIFICATE",
					Bytes: der,
				})),

				"tls.key": string(pem.EncodeToMemory(&pem.Block{
					Type:  "EC PRIVATE KEY",
					Bytes: privDer,
				})),
			},
		}
		err = m.client.Create(ctx, signedSecret)
		if err != nil {
			return err
		}

	} else if err != nil {
		return err
	}
	return nil
}

func getKubeconfig(apiServerPath string, kubeconfig string) (*rest.Config, error) {
	config, err := clientcmd.BuildConfigFromFlags(apiServerPath, kubeconfig)
	if err != nil {
		// Fall back to default client loading rules.
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		// if you want to change the loading rules (which files in which order), you can do so here
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, nil)
		return kubeConfig.ClientConfig()
	}
	return config, nil
}

// setupAnonymousReports sets up and starts the anonymous reporting and returns
// a cleanup function and an error.
// The caller is responsible to call the returned function - when the returned
// error is not nil - to stop the reports sending.
func setupAnonymousReports(cfg Config) (func(), error) {
	setupLog.Info("starting anonymous reports")
	restConfig, err := getKubeconfig(cfg.APIServerPath, cfg.KubeconfigPath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get kubeconfig")
	}

	telemetryPayload := telemetry.Payload{
		"v": metadata.Release,
	}

	tMgr, err := telemetry.CreateManager(telemetry.SignalPing, restConfig, setupLog, telemetryPayload)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create anonymous reports manager")
	}

	if err := tMgr.Start(); err != nil {
		return nil, errors.Wrapf(err, "anonymous reports failed to start")
	}

	if err := tMgr.TriggerExecute(context.Background(), telemetry.SignalStart); err != nil {
		// We failed to send initial start signal with telemetry data.
		// Don't abort and return an error, just log an error and continue.
		setupLog.WithValues("error", err).
			Info("failed to send an initial telemetry start signal")
	}

	return tMgr.Stop, nil
}

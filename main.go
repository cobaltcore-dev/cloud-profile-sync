// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"os"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/controllers"
	"github.com/cobaltcore-dev/cloud-profile-sync/filewatcher"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gardenerv1beta1.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {
	var kubecontext string
	opts := zap.Options{
		Development: true,
		TimeEncoder: zapcore.ISO8601TimeEncoder,
	}
	flag.StringVar(&kubecontext, "kubecontext", "", "The context to use from the kubeconfig (defaults to current-context)")
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	ctx := ctrl.SetupSignalHandler()

	filewatcher.RerunOnFileUpdate(ctx, getWatchPath(), func(ctx context.Context) {
		restConfig := getKubeconfigOrDie(kubecontext)
		setupLog.Info("loaded kubeconfig", "context", kubecontext, "host", restConfig.Host)

		mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
			Scheme:         scheme,
			LeaderElection: false,
		})
		if err != nil {
			setupLog.Error(err, "unable to start manager")
			os.Exit(1)
		}

		reconciler := controllers.Reconciler{
			Client: mgr.GetClient(),
		}
		if err := reconciler.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "problem setting up ManagedCloudProfile reconciler")
			os.Exit(1)
		}

		setupLog.Info("starting manager")
		if err := mgr.Start(ctx); err != nil {
			setupLog.Error(err, "problem running manager")
			os.Exit(1)
		}
		setupLog.Info("received SIGTERM or SIGINT. See you later.")
	})
}

const inClusterCertPath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

func getWatchPath() string {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return inClusterCertPath
	}

	return clientcmd.NewDefaultClientConfigLoadingRules().GetLoadingPrecedence()[0]
}

func getKubeconfigOrDie(kubecontext string) *rest.Config {
	if kubecontext == "" {
		kubecontext = os.Getenv("KUBECONTEXT")
	}
	restConfig, err := ctrlconfig.GetConfigWithContext(kubecontext)
	if err != nil {
		setupLog.Error(err, "Failed to load kubeconfig")
		os.Exit(1)
	}
	return restConfig
}

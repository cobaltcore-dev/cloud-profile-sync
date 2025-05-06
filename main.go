// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"os"

	"github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

const configPath = "config/config.json"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1beta1.AddToScheme(scheme))
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

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		setupLog.Error(err, "unable to read config file")
		os.Exit(1)
	}
	config, err := cloudprofilesync.LoadConfig(configBytes)
	if err != nil {
		setupLog.Error(err, "unable to load config")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	if err := mgr.Add(&cloudprofilesync.Runnable{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("cloudprofilesync"),
		Source:   config.Source,
		Provider: config.Provider,
		Profile:  types.NamespacedName{Name: config.CloudProfile},
	}); err != nil {
		setupLog.Error(err, "unable to add runnable to manager")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
	setupLog.Info("received SIGTERM or SIGINT. See you later.")
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

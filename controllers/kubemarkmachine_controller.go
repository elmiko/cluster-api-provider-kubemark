/*


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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	certificates "k8s.io/api/certificates/v1beta1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	certificatesclient "k8s.io/client-go/kubernetes/typed/certificates/v1beta1"
	restclient "k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/certificate"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	infrav1 "github.com/benmoss/cluster-api-provider-kubemark/api/v1alpha3"
	capkcert "github.com/benmoss/cluster-api-provider-kubemark/util/certificate"
)

const (
	kubeconfigPath = "/etc/kubernetes/kubelet.conf"
)

var (
	hostPathFile = v1.HostPathFile
)

// KubemarkMachineReconciler reconciles a KubemarkMachine object
type KubemarkMachineReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubemarkmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubemarkmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;,verbs=get;list;watch
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=kubeadmconfigs,verbs=get;list;watch

func (r *KubemarkMachineReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	logger := r.Log.WithValues("kubemarkmachine", req.NamespacedName)

	kubemarkMachine := &infrav1.KubemarkMachine{}
	err := r.Get(ctx, req.NamespacedName, kubemarkMachine)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "error finding kubemark machine")
		return ctrl.Result{}, err
	}

	// Fetch the Machine.
	machine, err := util.GetOwnerMachine(ctx, r, kubemarkMachine.ObjectMeta)
	if err != nil {
		logger.Error(err, "error finding owner machine")
		return ctrl.Result{}, err
	}
	if machine == nil {
		logger.Info("Machine Controller has not yet set OwnerRef")
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("machine", machine.Name)

	// Fetch the Cluster.
	cluster, err := util.GetClusterFromMetadata(ctx, r, machine.ObjectMeta)
	if err != nil {
		logger.Info("Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, nil
	}
	restConfig, err := remote.RESTConfig(ctx, r.Client, util.ObjectKey(cluster))
	if err != nil {
		logger.Error(err, "error getting restconfig")
		return ctrl.Result{}, err
	}
	restConfig.Timeout = 30 * time.Second

	c, err := client.New(restConfig, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		logger.Error(err, "error creating client")
		return ctrl.Result{}, err
	}

	if !cluster.Status.InfrastructureReady {
		logger.Info("Cluster infrastructure is not ready yet")
		return ctrl.Result{}, nil
	}
	if machine.Spec.Bootstrap.DataSecretName == nil {
		logger.Info("Bootstrap data secret reference is not yet available")
		return ctrl.Result{}, nil
	}

	var kubeadmConfig bootstrapv1.KubeadmConfig
	if err := r.Get(context.TODO(), types.NamespacedName{
		Name:      machine.Spec.Bootstrap.ConfigRef.Name,
		Namespace: machine.Spec.Bootstrap.ConfigRef.Namespace,
	}, &kubeadmConfig); err != nil {
		logger.Error(err, "error getting bootstrap config")
		return ctrl.Result{}, err
	}

	cfg, err := RetrieveValidatedConfigInfo(kubeadmConfig.Spec.JoinConfiguration)
	if err != nil {
		logger.Error(err, "error validating token")
		return ctrl.Result{}, err
	}

	clusterinfo := cfg.Clusters[""]
	cfg = CreateWithToken(
		clusterinfo.Server,
		DefaultClusterName,
		TokenUser,
		clusterinfo.CertificateAuthorityData,
		kubeadmConfig.Spec.JoinConfiguration.Discovery.BootstrapToken.Token,
	)
	certificateStore := &capkcert.MemoryStore{}

	newClientFn := func(current *tls.Certificate) (certificatesclient.CertificateSigningRequestInterface, error) {
		// If we have a valid certificate, use that to fetch CSRs. Otherwise use the bootstrap
		// credentials. In the future it would be desirable to change the behavior of bootstrap
		// to always fall back to the external bootstrap credentials when such credentials are
		// provided by a fundamental trust system like cloud VM identity or an HSM module.
		client, err := clientset.NewForConfig(restConfig)
		if err != nil {
			logger.Error(err, "error creating clientset")
			return nil, err
		}
		return client.CertificatesV1beta1().CertificateSigningRequests(), nil
	}
	mgr, err := certificate.NewManager(&certificate.Config{
		BootstrapCertificatePEM: cfg.AuthInfos[TokenUser].ClientCertificateData,
		BootstrapKeyPEM:         cfg.AuthInfos[TokenUser].ClientKeyData,
		CertificateStore:        certificateStore,
		Template: &x509.CertificateRequest{
			Subject: pkix.Name{
				CommonName:   fmt.Sprintf("system:node:%s", kubemarkMachine.Name),
				Organization: []string{"system:nodes"},
			},
		},
		Usages: []certificates.KeyUsage{
			certificates.UsageDigitalSignature,
			certificates.UsageKeyEncipherment,
			certificates.UsageClientAuth,
		},
		ClientFn: newClientFn,
	})
	if err != nil {
		logger.Error(err, "error creating cert manager")
		return ctrl.Result{}, err
	}

	mgr.Start()

	for {
		_, err := certificateStore.Current()
		if err != nil {
			if _, ok := err.(*certificate.NoCertKeyError); !ok {
				logger.Error(err, "err fetching certificate")
				return ctrl.Result{}, err
			}

			time.Sleep(time.Second)

			continue
		}

		break
	}
	mgr.Stop()

	kubeconfig, err := generateCertificateKubeconfig(restConfig, "/kubeconfig/cert.pem")
	if err != nil {
		logger.Error(err, "err generating certificate kubeconfig")
		return ctrl.Result{}, err
	}

	stackedCert := bytes.Buffer{}
	if err := pem.Encode(&stackedCert, &pem.Block{Type: cert.CertificateBlockType, Bytes: certificateStore.Certificate.Leaf.Raw}); err != nil {
		logger.Error(err, "err encoding certificate")
		return ctrl.Result{}, err
	}
	keyBytes, err := keyutil.MarshalPrivateKeyToPEM(certificateStore.Certificate.PrivateKey)
	if err != nil {
		logger.Error(err, "err encoding key")
		return ctrl.Result{}, err
	}
	stackedCert.Write(keyBytes)

	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubemarkMachine.ObjectMeta.Name,
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"kubeconfig": string(kubeconfig),
			"cert.pem":   string(stackedCert.Bytes()),
		},
	}
	if err := c.Create(context.TODO(), configMap); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			logger.Error(err, "failed to create configmap")
			return ctrl.Result{}, err
		}
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubemarkMachine.Name,
			Labels:    map[string]string{"app": kubemarkName},
			Namespace: "kube-system",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": kubemarkName,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": kubemarkName,
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  kubemarkName,
							Image: "gcr.io/cf-london-servces-k8s/bmo/kubemark@sha256:9f717e0f2fc1b00c72719f157c1a3846ab8180070c201b950cade504c12dec59",
							Args: []string{
								"--v=3",
								"--morph=kubelet",
								"--log-file=/var/log/kubelet.log",
								"--logtostderr=false",
								"--register-with-taints=kubemark=true:NoSchedule",
								fmt.Sprintf("--name=%s", kubemarkMachine.Name),
							},
							Command: []string{"/kubemark"},
							SecurityContext: &v1.SecurityContext{
								Privileged: pointer.BoolPtr(true),
							},
							VolumeMounts: []v1.VolumeMount{
								{
									MountPath: "/kubeconfig",
									Name:      "kubeconfig",
								},
							},
						},
					},
					Tolerations: []v1.Toleration{
						{
							Key:    "node-role.kubernetes.io/master",
							Effect: v1.TaintEffectNoSchedule,
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "kubeconfig",
							VolumeSource: v1.VolumeSource{
								ConfigMap: &v1.ConfigMapVolumeSource{
									LocalObjectReference: v1.LocalObjectReference{Name: configMap.Name},
								},
							},
						},
					},
				},
			},
		},
	}

	if err = c.Create(context.TODO(), deployment); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			logger.Error(err, "failed to create deployment")
			return ctrl.Result{}, err
		}
	}

	machine.Spec.ProviderID = pointer.StringPtr(fmt.Sprintf("kubemark://%s", kubemarkMachine.Name))
	if err := r.Update(context.TODO(), machine); err != nil {
		logger.Error(err, "failed to update machine")
		return ctrl.Result{}, err
	}

	kubemarkMachine.Status.Ready = true
	if err := r.Update(context.TODO(), kubemarkMachine); err != nil {
		logger.Error(err, "failed to update KubemarkMachine")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *KubemarkMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.KubemarkMachine{}).
		Watches(
			&source.Kind{Type: &clusterv1.Machine{}},
			&handler.EnqueueRequestsFromMapFunc{
				ToRequests: util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("AWSMachine")),
			},
		).
		Complete(r)
}

func generateCertificateKubeconfig(bootstrapClientConfig *restclient.Config, pemPath string) ([]byte, error) {
	// Get the CA data from the bootstrap client config.
	caFile, caData := bootstrapClientConfig.CAFile, []byte{}
	if len(caFile) == 0 {
		caData = bootstrapClientConfig.CAData
	}

	// Build resulting kubeconfig.
	kubeconfigData := &clientcmdapi.Config{
		// Define a cluster stanza based on the bootstrap kubeconfig.
		Clusters: map[string]*clientcmdapi.Cluster{"default-cluster": {
			Server:                   bootstrapClientConfig.Host,
			InsecureSkipTLSVerify:    bootstrapClientConfig.Insecure,
			CertificateAuthority:     caFile,
			CertificateAuthorityData: caData,
		}},
		// Define auth based on the obtained client cert.
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"default-auth": {
			ClientCertificate: pemPath,
			ClientKey:         pemPath,
		}},
		// Define a context that connects the auth info and cluster, and set it as the default
		Contexts: map[string]*clientcmdapi.Context{"default-context": {
			Cluster:   "default-cluster",
			AuthInfo:  "default-auth",
			Namespace: "default",
		}},
		CurrentContext: "default-context",
	}

	// Marshal to disk
	return runtime.Encode(clientcmdlatest.Codec, kubeconfigData)
}

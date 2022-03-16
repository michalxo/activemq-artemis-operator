/*
Copyright 2021.

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
	"reflect"

	brokerv1beta1 "github.com/artemiscloud/activemq-artemis-operator/api/v1beta1"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/resources"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/resources/environments"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/resources/secrets"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/common"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/lsrcrs"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/random"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/selectors"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var elog = ctrl.Log.WithName("controller_v1alpha1activemqartemissecurity")

// ActiveMQArtemisSecurityReconciler reconciles a ActiveMQArtemisSecurity object
type ActiveMQArtemisSecurityReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=broker.amq.io,resources=activemqartemissecurities,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=broker.amq.io,resources=activemqartemissecurities/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=broker.amq.io,resources=activemqartemissecurities/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ActiveMQArtemisSecurity object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *ActiveMQArtemisSecurityReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {

	reqLogger := ctrl.Log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling ActiveMQArtemisSecurity")

	instance := &brokerv1beta1.ActiveMQArtemisSecurity{}

	if err := r.Client.Get(context.TODO(), request.NamespacedName, instance); err != nil {
		if errors.IsNotFound(err) {
			//unregister the CR
			RemoveBrokerConfigHandler(request.NamespacedName)
			// Setting err to nil to prevent requeue
			err = nil
			//clean the CR
			lsrcrs.DeleteLastSuccessfulReconciledCR(request.NamespacedName, "security", getLabels(instance), r.Client)
		} else {
			reqLogger.Error(err, "Reconcile errored thats not IsNotFound, requeuing request", "Request Namespace", request.Namespace, "Request Name", request.Name)
		}

		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: common.GetReconcileResyncPeriod()}, nil
	}

	toReconcile := true
	newHandler := &ActiveMQArtemisSecurityConfigHandler{
		instance,
		request.NamespacedName,
		r,
	}

	if securityHandler := GetBrokerConfigHandler(request.NamespacedName); securityHandler == nil {
		reqLogger.Info("Operator doesn't have the security handler, try retrive it from secret")
		if existingHandler := lsrcrs.RetrieveLastSuccessfulReconciledCR(request.NamespacedName, "security", r.Client, getLabels(instance)); existingHandler != nil {
			//compare resource version
			if existingHandler.Checksum == instance.ResourceVersion {
				reqLogger.V(1).Info("The incoming security CR is identical to stored CR, no reconcile")
				toReconcile = false
			}
		}
	} else {
		if reflect.DeepEqual(securityHandler, newHandler) {
			reqLogger.Info("Will not reconcile the same security config")
			return ctrl.Result{RequeueAfter: common.GetReconcileResyncPeriod()}, nil
		}
	}

	if err := AddBrokerConfigHandler(request.NamespacedName, newHandler, toReconcile); err != nil {
		reqLogger.Error(err, "failed to config security cr", "request", request.NamespacedName)
		return ctrl.Result{}, err
	}
	//persist the CR
	crstr, merr := common.ToJson(instance)
	if merr != nil {
		reqLogger.Error(merr, "failed to marshal cr")
	}

	lsrcrs.StoreLastSuccessfulReconciledCR(instance, instance.Name, instance.Namespace, "security",
		crstr, "", instance.ResourceVersion, getLabels(instance), r.Client, r.Scheme)

	return ctrl.Result{RequeueAfter: common.GetReconcileResyncPeriod()}, nil
}

type ActiveMQArtemisSecurityConfigHandler struct {
	SecurityCR     *brokerv1beta1.ActiveMQArtemisSecurity
	NamespacedName types.NamespacedName
	owner          *ActiveMQArtemisSecurityReconciler
}

func getLabels(cr *brokerv1beta1.ActiveMQArtemisSecurity) map[string]string {
	labelBuilder := selectors.LabelerData{}
	labelBuilder.Base(cr.Name).Suffix("sec").Generate()
	return labelBuilder.Labels()
}

func (r *ActiveMQArtemisSecurityConfigHandler) IsApplicableFor(brokerNamespacedName types.NamespacedName) bool {
	applyTo := r.SecurityCR.Spec.ApplyToCrNames
	if len(applyTo) == 0 {
		return true
	}
	for _, crName := range applyTo {
		if crName == "*" || crName == "" || (r.NamespacedName.Namespace == brokerNamespacedName.Namespace && crName == brokerNamespacedName.Name) {
			return true
		}
	}
	return false
}

func (r *ActiveMQArtemisSecurityConfigHandler) processCrPasswords() *brokerv1beta1.ActiveMQArtemisSecurity {
	result := r.SecurityCR.DeepCopy()

	if len(result.Spec.LoginModules.PropertiesLoginModules) > 0 {
		for i, pm := range result.Spec.LoginModules.PropertiesLoginModules {
			if len(pm.Users) > 0 {
				for j, user := range pm.Users {
					if user.Password == nil {
						result.Spec.LoginModules.PropertiesLoginModules[i].Users[j].Password = r.getPassword("security-properties-"+pm.Name, user.Name)
					}
				}
			}
		}
	}

	if len(result.Spec.LoginModules.KeycloakLoginModules) > 0 {
		for _, pm := range result.Spec.LoginModules.KeycloakLoginModules {
			keycloakSecretName := "security-keycloak-" + pm.Name
			if pm.Configuration.ClientKeyStore != nil {
				if pm.Configuration.ClientKeyPassword == nil {
					pm.Configuration.ClientKeyPassword = r.getPassword(keycloakSecretName, "client-key-password")
				}
				if pm.Configuration.ClientKeyStorePassword == nil {
					pm.Configuration.ClientKeyStorePassword = r.getPassword(keycloakSecretName, "client-key-store-password")
				}
			}
			if pm.Configuration.TrustStore != nil {
				if pm.Configuration.TrustStorePassword == nil {
					pm.Configuration.TrustStorePassword = r.getPassword(keycloakSecretName, "trust-store-password")
				}
			}
			//need to process pm.Configuration.Credentials too. later.
			if len(pm.Configuration.Credentials) > 0 {
				for i, kv := range pm.Configuration.Credentials {
					if kv.Value == nil {
						pm.Configuration.Credentials[i].Value = r.getPassword(keycloakSecretName, "credentials-"+kv.Key)
					}
				}
			}
		}
	}
	return result
}

func (r *ActiveMQArtemisSecurityConfigHandler) GetDefaultLabels() map[string]string {
	defaultLabelData := selectors.LabelerData{}
	defaultLabelData.Base(r.SecurityCR.Name).Suffix("app").Generate()
	return defaultLabelData.Labels()

}

//retrive value from secret, generate value if not exist.
func (r *ActiveMQArtemisSecurityConfigHandler) getPassword(secretName string, key string) *string {
	//check if the secret exists.
	namespacedName := types.NamespacedName{
		Name:      secretName,
		Namespace: r.NamespacedName.Namespace,
	}
	// Attempt to retrieve the secret
	stringDataMap := make(map[string]string)

	secretDefinition := secrets.NewSecret(namespacedName, secretName, stringDataMap, r.GetDefaultLabels())

	if err := resources.Retrieve(namespacedName, r.owner.Client, secretDefinition); err != nil {
		if errors.IsNotFound(err) {
			//create the secret
			resources.Create(r.SecurityCR, namespacedName, r.owner.Client, r.owner.Scheme, secretDefinition)
		}
	} else {
		slog.Info("Found secret " + secretName)

		if elem, ok := secretDefinition.Data[key]; ok {
			//the value exists
			value := string(elem)
			return &value
		}
	}
	//now need generate value
	value := random.GenerateRandomString(8)
	//update the secret
	if secretDefinition.Data == nil {
		secretDefinition.Data = make(map[string][]byte)
	}
	secretDefinition.Data[key] = []byte(value)
	slog.Info("Updating secret", "secret", namespacedName.Name)
	if err := resources.Update(namespacedName, r.owner.Client, secretDefinition); err != nil {
		slog.Error(err, "failed to update secret", "secret", secretName)
	}
	return &value
}

func (r *ActiveMQArtemisSecurityConfigHandler) Config(initContainers []corev1.Container, outputDirRoot string, yacfgProfileVersion string, yacfgProfileName string) (value []string) {
	slog.Info("Reconciling security", "cr", r.SecurityCR)
	result := r.processCrPasswords()
	outputDir := outputDirRoot + "/security"
	var configCmds = []string{"echo \"making dir " + outputDir + "\"", "mkdir -p " + outputDir}
	filePath := outputDir + "/security-config.yaml"
	cmdPersistCRAsYaml, err := r.persistCR(filePath, result)
	if err != nil {
		slog.Error(err, "Error marshalling security CR", "cr", r.SecurityCR)
		return nil
	}
	slog.Info("get the command", "value", cmdPersistCRAsYaml)
	configCmds = append(configCmds, cmdPersistCRAsYaml)
	configCmds = append(configCmds, "/opt/amq-broker/script/cfg/config-security.sh")
	envVarName := "SECURITY_CFG_YAML"
	envVar := corev1.EnvVar{
		envVarName,
		filePath,
		nil,
	}
	environments.Create(initContainers, &envVar)

	envVarName = "YACFG_PROFILE_VERSION"
	envVar = corev1.EnvVar{
		envVarName,
		yacfgProfileVersion,
		nil,
	}
	environments.Create(initContainers, &envVar)

	envVarName = "YACFG_PROFILE_NAME"
	envVar = corev1.EnvVar{
		envVarName,
		yacfgProfileName,
		nil,
	}
	environments.Create(initContainers, &envVar)

	slog.Info("returning config cmds", "value", configCmds)
	return configCmds
}

func (r *ActiveMQArtemisSecurityConfigHandler) persistCR(filePath string, cr *brokerv1beta1.ActiveMQArtemisSecurity) (value string, err error) {

	data, err := yaml.Marshal(cr)
	if err != nil {
		return "", err
	}
	return "echo \"" + string(data) + "\" > " + filePath, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ActiveMQArtemisSecurityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&brokerv1beta1.ActiveMQArtemisSecurity{}).
		Complete(r)
}

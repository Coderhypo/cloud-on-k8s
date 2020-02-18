package enterprisesearch

import (
	"fmt"
	"path/filepath"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	commonv1 "github.com/elastic/cloud-on-k8s/pkg/apis/common/v1"
	entsv1beta1 "github.com/elastic/cloud-on-k8s/pkg/apis/enterprisesearch/v1beta1"
	"github.com/elastic/cloud-on-k8s/pkg/controller/common/association"
	"github.com/elastic/cloud-on-k8s/pkg/controller/common/certificates"
	"github.com/elastic/cloud-on-k8s/pkg/controller/common/reconciler"
	"github.com/elastic/cloud-on-k8s/pkg/controller/common/settings"
	"github.com/elastic/cloud-on-k8s/pkg/controller/common/volume"
	"github.com/elastic/cloud-on-k8s/pkg/utils/k8s"
	entsname "github.com/elastic/cloud-on-k8s/pkg/controller/enterprisesearch/name"
)

const (
	// TODO: homogenize mount path with ES, Kibana, etc.?
	ESCertsPath = "/mnt/es-certs"
	ConfigFilename = "enterprise-search.yml"
	ConfigMountPath = "/mnt/config"
)

func ConfigSecretVolume(ents entsv1beta1.EnterpriseSearch) volume.SecretVolume {
	return volume.NewSecretVolumeWithMountPath(entsname.Config(ents.Name), "config", ConfigMountPath)
}

// Reconcile reconciles the configuration of Enterprise Search: it generates the right configuration and
// stores it in a secret that is kept up to date.
func ReconcileConfig(client k8s.Client, scheme *runtime.Scheme, ents entsv1beta1.EnterpriseSearch) (*corev1.Secret, error) {
	cfg, err := newConfig(client, ents)
	if err != nil {
		return nil, err
	}

	cfgBytes, err := cfg.Render()
	if err != nil {
		return nil, err
	}

	// Reconcile the configuration in a secret
	expectedConfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ents.Namespace,
			Name:      entsname.Config(ents.Name),
			Labels:    NewLabels(ents.Name),
		},
		Data: map[string][]byte{
			ConfigFilename: cfgBytes,
		},
	}

	reconciledConfigSecret := &corev1.Secret{}
	if err := reconciler.ReconcileResource(
		reconciler.Params{
			Client: client,
			Scheme: scheme,
			Owner:      &ents,
			Expected:   expectedConfigSecret,
			Reconciled: reconciledConfigSecret,
			NeedsUpdate: func() bool {
				return !reflect.DeepEqual(reconciledConfigSecret.Data, expectedConfigSecret.Data) ||
					!reflect.DeepEqual(reconciledConfigSecret.Labels, expectedConfigSecret.Labels)
			},
			UpdateReconciled: func() {
				reconciledConfigSecret.Labels = expectedConfigSecret.Labels
				reconciledConfigSecret.Data = expectedConfigSecret.Data
			},
		},
	); err != nil {
		return nil, err
	}
	return reconciledConfigSecret, nil
}



func newConfig(c k8s.Client, ents entsv1beta1.EnterpriseSearch) (*settings.CanonicalConfig, error) {
	cfg := defaultConfig()

	specConfig := ents.Spec.Config
	if specConfig == nil {
		specConfig = &commonv1.Config{}
	}
	userProvidedCfg, err := settings.NewCanonicalConfigFrom(specConfig.Data)
	if err != nil {
		return nil, err
	}

	associationCfg, err := associationConfig(c, ents)
	if err != nil {
		return nil, err
	}

	// merge with user settings last so they take precedence
	if err := cfg.MergeWith(associationCfg, userProvidedCfg); err != nil {
		return nil, err
	}
	return cfg, nil
}


func defaultConfig() *settings.CanonicalConfig {
	return settings.MustCanonicalConfig(map[string]interface{}{
		"ent_search.external_url": fmt.Sprintf("http://localhost:%d", HTTPPort),
		"ent_search.listen_host": "0.0.0.0",
		"allow_es_settings_modification": true,
		// TODO explicitly handle those two
		"secret_session_key": "TODOCHANGEMEsecret_session_key",
		"secret_management.encryption_keys": []string{"TODOCHANGEMEsecret_management.encryption_keys"},
	})
}

func associationConfig(c k8s.Client, ents entsv1beta1.EnterpriseSearch) (*settings.CanonicalConfig, error) {
	if !ents.AssociationConf().IsConfigured() {
		return settings.NewCanonicalConfig(), nil
	}

	username, password, err := association.ElasticsearchAuthSettings(c, &ents)
	if err != nil {
		return nil, err
	}
	cfg :=  settings.MustCanonicalConfig(map[string]string{
		"ent_search.auth.source": "elasticsearch-native",
		"elasticsearch.host": ents.AssociationConf().URL,
		"elasticsearch.username": username,
		"elasticsearch.password": password,
	})

	if ents.AssociationConf().CAIsConfigured() {
		cfg.MergeWith(settings.MustCanonicalConfig(map[string]interface{}{
			"elasticsearch.ssl.enabled": true,
			"elasticsearch.ssl.certificate_authority": filepath.Join(ESCertsPath, certificates.CertFileName),
		}))
	}
	return cfg, nil
}


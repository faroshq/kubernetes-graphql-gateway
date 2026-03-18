package v1alpha1

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kube-openapi/pkg/spec3"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Schema represents the data extracted from a schema file
// +kubebuilder:object:generate=false
type Schema struct {
	Components      *spec3.Components `json:"components,omitempty"`
	ClusterMetadata *ClusterMetadata  `json:"x-cluster-metadata,omitempty"`
}

// ClusterMetadataFunc is a function type that returns ClusterMetadata for a given cluster name
// +kubebuilder:object:generate=false
type ClusterMetadataFunc func(clusterName string) (*ClusterMetadata, error)

// ClusterURLResolver is function that will resolve cluster url for a given cluster name
// +kubebuilder:object:generate=false
type ClusterURLResolver func(currentURL, clusterName string) (string, error)

// DefaultClusterURLResolverFunc is the default implementation that returns the URL unchanged
func DefaultClusterURLResolverFunc(url, clusterName string) (string, error) {
	return url, nil
}

// These following types are used to store cluster connection metadata in schema files
// They are not used directly in Kubernetes resources.

// ClusterMetadata represents the cluster connection metadata stored in schema files.
type ClusterMetadata struct {
	Host string        `json:"host"`
	Path string        `json:"path,omitempty"`
	Auth *AuthMetadata `json:"auth,omitempty"`
	CA   *CAMetadata   `json:"ca,omitempty"`
}

type AuthenticationType string

const (
	AuthTypeToken      AuthenticationType = "token"
	AuthTypeKubeconfig AuthenticationType = "kubeconfig"
	AuthTypeClientCert AuthenticationType = "clientCert"
)

// AuthMetadata represents authentication information
type AuthMetadata struct {
	Type       AuthenticationType `json:"type"`
	Token      string             `json:"token,omitempty"`
	Kubeconfig string             `json:"kubeconfig,omitempty"`
	CertData   string             `json:"certData,omitempty"`
	KeyData    string             `json:"keyData,omitempty"`
	// ServiceAccount fields for SA token generation
	SAName      string   `json:"saName,omitempty"`
	SANamespace string   `json:"saNamespace,omitempty"`
	SAAudience  []string `json:"saAudience,omitempty"`
}

// CAMetadata represents CA certificate information
type CAMetadata struct {
	Data string `json:"data"`
}

// buildConfigFromMetadata creates a rest.Config from base64-encoded metadata (used by gateway)
func BuildRestConfigFromMetadata(metadata ClusterMetadata) (*rest.Config, error) {
	return buildConfigFromMetadata(metadata)
}

// BuildClusterMetadataFromClusterAccess builds ClusterMetadata from ClusterAccess by reading secrets
func BuildClusterMetadataFromClusterAccess(ctx context.Context, ca ClusterAccess, c client.Client) (*ClusterMetadata, error) {
	return buildClusterMetadataFromClusterAccess(ctx, ca, c)
}

// buildClusterMetadataFromClusterAccess builds ClusterMetadata from ClusterAccess
func buildClusterMetadataFromClusterAccess(ctx context.Context, ca ClusterAccess, c client.Client) (*ClusterMetadata, error) {
	metadata := &ClusterMetadata{
		Host: ca.Spec.Host,
		Path: ca.Spec.Path,
	}

	// Handle CA configuration
	if ca.Spec.CA != nil && ca.Spec.CA.SecretRef != nil {
		caData, err := readSecretKey(ctx, c, ca.Spec.CA.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA secret: %w", err)
		}
		metadata.CA = &CAMetadata{
			Data: base64.StdEncoding.EncodeToString(caData),
		}
	}

	// Handle authentication configuration
	if ca.Spec.Auth == nil {
		return metadata, nil
	}

	auth := ca.Spec.Auth
	switch {
	case auth.TokenSecretRef != nil:
		tokenData, err := readSecretKey(ctx, c, auth.TokenSecretRef)
		if err != nil {
			return nil, fmt.Errorf("failed to read token secret: %w", err)
		}
		metadata.Auth = &AuthMetadata{
			Type:  AuthTypeToken,
			Token: base64.StdEncoding.EncodeToString(tokenData),
		}

	case auth.KubeconfigSecretRef != nil:
		kubeconfigData, err := readSecretKey(ctx, c, auth.KubeconfigSecretRef)
		if err != nil {
			return nil, fmt.Errorf("failed to read kubeconfig secret: %w", err)
		}
		metadata.Auth = &AuthMetadata{
			Type:       AuthTypeKubeconfig,
			Kubeconfig: base64.StdEncoding.EncodeToString(kubeconfigData),
		}

	case auth.ClientCertificateRef != nil:
		secret := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{
			Name:      auth.ClientCertificateRef.Name,
			Namespace: auth.ClientCertificateRef.Namespace,
		}, secret); err != nil {
			return nil, fmt.Errorf("failed to get client certificate secret: %w", err)
		}

		certData, ok := secret.Data[corev1.TLSCertKey]
		if !ok {
			return nil, fmt.Errorf("secret %s/%s missing key %s", auth.ClientCertificateRef.Namespace, auth.ClientCertificateRef.Name, corev1.TLSCertKey)
		}
		keyData, ok := secret.Data[corev1.TLSPrivateKeyKey]
		if !ok {
			return nil, fmt.Errorf("secret %s/%s missing key %s", auth.ClientCertificateRef.Namespace, auth.ClientCertificateRef.Name, corev1.TLSPrivateKeyKey)
		}

		metadata.Auth = &AuthMetadata{
			Type:     AuthTypeClientCert,
			CertData: base64.StdEncoding.EncodeToString(certData),
			KeyData:  base64.StdEncoding.EncodeToString(keyData),
		}

	case auth.ServiceAccountRef != nil:
		// Generate a token for the ServiceAccount using TokenRequest API
		sa := &corev1.ServiceAccount{}
		if err := c.Get(ctx, client.ObjectKey{
			Name:      auth.ServiceAccountRef.Name,
			Namespace: auth.ServiceAccountRef.Namespace,
		}, sa); err != nil {
			return nil, fmt.Errorf("failed to get service account %s/%s: %w", auth.ServiceAccountRef.Namespace, auth.ServiceAccountRef.Name, err)
		}

		tokenRequest := &authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				Audiences: auth.ServiceAccountRef.Audience,
			},
		}

		// Use configured expiration if provided
		if auth.ServiceAccountRef.TokenExpiration != nil && auth.ServiceAccountRef.TokenExpiration.Duration > 0 {
			expirationSeconds := int64(auth.ServiceAccountRef.TokenExpiration.Seconds())
			tokenRequest.Spec.ExpirationSeconds = &expirationSeconds
		}

		if err := c.SubResource("token").Create(ctx, sa, tokenRequest); err != nil {
			return nil, fmt.Errorf("failed to create token for service account %s/%s: %w", auth.ServiceAccountRef.Namespace, auth.ServiceAccountRef.Name, err)
		}

		metadata.Auth = &AuthMetadata{
			Type:        AuthTypeServiceAccount,
			Token:       base64.StdEncoding.EncodeToString([]byte(tokenRequest.Status.Token)),
			SAName:      auth.ServiceAccountRef.Name,
			SANamespace: auth.ServiceAccountRef.Namespace,
			SAAudience:  auth.ServiceAccountRef.Audience,
		}
	}

	return metadata, nil
}

// readSecretKey reads a specific key from a secret referenced by SecretKeyRef
func readSecretKey(ctx context.Context, c client.Client, ref *SecretKeyRef) ([]byte, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{
		Name:      ref.Name,
		Namespace: ref.Namespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	key := ref.Key
	if key == "" {
		// Use default key based on common conventions
		if _, ok := secret.Data[corev1.ServiceAccountTokenKey]; ok {
			key = corev1.ServiceAccountTokenKey
		} else {
			// Return the first key if there's only one
			if len(secret.Data) == 1 {
				for k := range secret.Data {
					key = k
					break
				}
			} else {
				return nil, fmt.Errorf("secret %s/%s has multiple keys, please specify which key to use", ref.Namespace, ref.Name)
			}
		}
	}

	data, ok := secret.Data[key]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s missing key %s", ref.Namespace, ref.Name, key)
	}

	return data, nil
}

// BuildRestConfigFromClusterAccess creates a rest.Config from ClusterAccess by reading secrets
func BuildRestConfigFromClusterAccess(ctx context.Context, ca ClusterAccess, c client.Client) (*rest.Config, error) {
	metadata, err := buildClusterMetadataFromClusterAccess(ctx, ca, c)
	if err != nil {
		return nil, err
	}
	return buildConfigFromMetadata(*metadata)
}

// buildConfigFromMetadata creates a rest.Config from base64-encoded metadata (used by gateway)
func buildConfigFromMetadata(metadata ClusterMetadata) (*rest.Config, error) {
	if metadata.Host == "" {
		return nil, errors.New("host is required")
	}

	config := &rest.Config{
		Host: metadata.Host,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true, // Start with insecure, will be overridden if CA is provided
		},
	}

	// Handle CA data
	if metadata.CA != nil && metadata.CA.Data != "" {
		decodedCA, err := base64.StdEncoding.DecodeString(metadata.CA.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to decode CA data: %w", err)
		}
		config.CAData = decodedCA
		config.Insecure = false
	}

	// Handle authentication based on type if we have it
	if metadata.Auth == nil {
		return config, nil
	}
	switch metadata.Auth.Type {
	case AuthTypeToken:
		if metadata.Auth.Token != "" {
			tokenData, err := base64.StdEncoding.DecodeString(metadata.Auth.Token)
			if err != nil {
				return nil, fmt.Errorf("failed to decode token: %w", err)
			}
			config.BearerToken = string(tokenData)
		}
	case AuthTypeKubeconfig:
		if metadata.Auth.Kubeconfig != "" {
			kubeconfigData, err := base64.StdEncoding.DecodeString(metadata.Auth.Kubeconfig)
			if err != nil {
				return nil, fmt.Errorf("failed to decode kubeconfig: %w", err)
			}

			if err := configureFromKubeconfig(config, kubeconfigData); err != nil {
				return nil, fmt.Errorf("failed to configure from kubeconfig: %w", err)
			}
		}
	case AuthTypeClientCert:
		if metadata.Auth.CertData != "" && metadata.Auth.KeyData != "" {
			decodedCert, err := base64.StdEncoding.DecodeString(metadata.Auth.CertData)
			if err != nil {
				return nil, fmt.Errorf("failed to decode cert data: %w", err)
			}
			decodedKey, err := base64.StdEncoding.DecodeString(metadata.Auth.KeyData)
			if err != nil {
				return nil, fmt.Errorf("failed to decode key data: %w", err)
			}
			config.CertData = decodedCert
			config.KeyData = decodedKey
		}
	case AuthTypeServiceAccount:
		// ServiceAccount auth stores a generated token for API access
		if metadata.Auth.Token != "" {
			tokenData, err := base64.StdEncoding.DecodeString(metadata.Auth.Token)
			if err != nil {
				return nil, fmt.Errorf("failed to decode service account token: %w", err)
			}
			config.BearerToken = string(tokenData)
		}
	}

	config.Host = metadata.Host

	return config, nil
}

// configureFromKubeconfig configures authentication from kubeconfig data
func configureFromKubeconfig(config *rest.Config, kubeconfigData []byte) error {
	// Parse kubeconfig and extract auth info
	clientConfig, err := clientcmd.NewClientConfigFromBytes(kubeconfigData)
	if err != nil {
		return errors.Join(errors.New("failed to parse kubeconfig"), err)
	}

	rawConfig, err := clientConfig.RawConfig()
	if err != nil {
		return errors.Join(errors.New("failed to get raw kubeconfig"), err)
	}

	// Get the current context
	currentContext := rawConfig.CurrentContext
	if currentContext == "" {
		return errors.New("no current context in kubeconfig")
	}

	context, exists := rawConfig.Contexts[currentContext]
	if !exists {
		return errors.New("current context not found in kubeconfig")
	}

	// Get auth info for current context
	authInfo, exists := rawConfig.AuthInfos[context.AuthInfo]
	if !exists {
		return errors.New("auth info not found in kubeconfig")
	}

	return extractAuthFromKubeconfig(config, authInfo)
}

// extractAuthFromKubeconfig extracts authentication info from kubeconfig AuthInfo
func extractAuthFromKubeconfig(config *rest.Config, authInfo *api.AuthInfo) error {
	if authInfo.Token != "" {
		config.BearerToken = authInfo.Token
		return nil
	}

	if authInfo.TokenFile != "" {
		// TODO: Read token from file if needed
		return errors.New("token file authentication not yet implemented")
	}

	if len(authInfo.ClientCertificateData) > 0 && len(authInfo.ClientKeyData) > 0 {
		config.CertData = authInfo.ClientCertificateData
		config.KeyData = authInfo.ClientKeyData
		return nil
	}

	if authInfo.ClientCertificate != "" && authInfo.ClientKey != "" {
		config.CertFile = authInfo.ClientCertificate
		config.KeyFile = authInfo.ClientKey
		return nil
	}

	if authInfo.Username != "" && authInfo.Password != "" {
		config.Username = authInfo.Username
		config.Password = authInfo.Password
		return nil
	}

	// No recognizable authentication found
	return errors.New("no valid authentication method found in kubeconfig")
}

// ClusterURLResolver is a function that resolves the cluster URL for a given cluster name.
type ClusterURLResolver func(currentURL, clusterName string) (string, error)

// DefaultClusterURLResolverFunc is the default implementation that returns the URL unchanged.
func DefaultClusterURLResolverFunc(url, clusterName string) (string, error) {
	return url, nil
}

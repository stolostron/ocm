package sscasigner

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	openshiftcrypto "github.com/openshift/library-go/pkg/crypto"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/cert"
)

var (
	rsaKeySize = 2048 // a decent number, as of 2019
	bigOne     = big.NewInt(1)
)

type CertRotation interface {
	EnsureTargetCertKeyPair(signingCertKeyPair *openshiftcrypto.CA, caBundleCerts []*x509.Certificate, fns ...openshiftcrypto.CertificateExtensionFunc) error
}

const (
	ClusterProxySignerCommonName = "open-cluster-management:cluster-proxy"
	ClusterProxySignerSecretName = "cluster-proxy-signer"
)

// SelfSignedCASigner is a certificate signer that uses a self-signed CA certificate.
//
// "Self-Signed CA" means the CA certificate is signed by its own private key, rather than
// by an external Certificate Authority. This is commonly used for:
//   - Internal/private PKI systems
//   - Development and testing environments
//   - Cluster-internal communications (like OCM's cluster-proxy)
//
// The SelfSignedCASigner manages the self-signed CA certificate and uses it to sign
// other certificates (which are NOT self-signed, but signed by this CA).
//
// Workflow:
//  1. Create or load a self-signed CA certificate
//  2. Use the CA to sign certificates for clients/servers
//  3. The signed certificates can be verified using the CA's public certificate
type SelfSignedCASigner interface {
	// Sign generates a new certificate signed by the self-signed CA.
	// The generated certificate is NOT self-signed; it's signed by the CA.
	Sign(cfg cert.Config, expiry time.Duration) (CertPair, error)

	// CAData returns the PEM-encoded CA certificate data.
	// This can be used to verify certificates signed by this CA.
	CAData() []byte

	// GetSigner returns the underlying crypto.Signer (private key) of the CA.
	GetSigner() crypto.Signer

	// CA returns the CA as an OpenShift TLS certificate configuration.
	CA() *openshiftcrypto.CA
}

var _ SelfSignedCASigner = &selfSignedCASigner{}

type selfSignedCASigner struct {
	caCert     *x509.Certificate
	caKey      crypto.Signer
	nextSerial *big.Int
}

// NewSelfSignedCASignerFromSecretOrGenerate loads a SelfSignedCASigner from the specified secret.
// If the secret does not exist, it generates a new self-signed CA signer and stores it in the secret.
// commonName is set as the CA certificate's Subject.CommonName field, used to identify the name of this CA (certificate authority).
func NewSelfSignedCASignerFromSecretOrGenerate(c kubernetes.Interface, secretNamespace string) (SelfSignedCASigner, error) {
	shouldGenerate := false
	caSecret, err := c.CoreV1().Secrets(secretNamespace).Get(context.TODO(), ClusterProxySignerSecretName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, errors.Wrapf(err, "failed to read ca from secret %v/%v", secretNamespace, ClusterProxySignerSecretName)
		}
		shouldGenerate = true
	}
	if !shouldGenerate {
		return newSelfSignedCASignerWithCAData(caSecret.Data[TLSCACert], caSecret.Data[TLSCAKey])
	}
	generatedSigner, err := newGeneratedSelfSignedCASigner(ClusterProxySignerCommonName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to generate new self-signed CA signer")
	}

	rawKeyData, err := x509.MarshalPKCS8PrivateKey(generatedSigner.GetSigner())
	if err != nil {
		return nil, err
	}
	caKeyData := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: rawKeyData,
	})
	isAlreadyExists, err := dumpCASecret(c,
		secretNamespace, ClusterProxySignerSecretName,
		generatedSigner.CAData(), caKeyData)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to dump generated ca secret %v/%v", secretNamespace, ClusterProxySignerSecretName)
	}
	if isAlreadyExists {
		return NewSelfSignedCASignerFromSecretOrGenerate(c, secretNamespace)
	}
	return generatedSigner, nil
}

func newGeneratedSelfSignedCASigner(commonName string) (SelfSignedCASigner, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, rsaKeySize)
	if err != nil {
		return nil, err
	}
	caCert, err := cert.NewSelfSignedCACert(cert.Config{
		CommonName: commonName,
	}, privateKey)
	if err != nil {
		return nil, err
	}
	return newSelfSignedCASignerWithCA(caCert, privateKey, big.NewInt(1))
}

func newSelfSignedCASignerWithCAData(caCertData, caKeyData []byte) (SelfSignedCASigner, error) {
	certBlock, _ := pem.Decode(caCertData)
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse ca certificate")
	}
	keyBlock, _ := pem.Decode(caKeyData)
	caKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse ca key")
	}
	next := big.NewInt(0)
	next.Add(caCert.SerialNumber, big.NewInt(1))
	return newSelfSignedCASignerWithCA(caCert, caKey.(*rsa.PrivateKey), next)
}

func newSelfSignedCASignerWithCA(caCert *x509.Certificate, caKey *rsa.PrivateKey, nextSerial *big.Int) (SelfSignedCASigner, error) {
	return &selfSignedCASigner{
		caCert:     caCert,
		caKey:      caKey,
		nextSerial: nextSerial,
	}, nil
}

func (s selfSignedCASigner) Sign(cfg cert.Config, expiry time.Duration) (CertPair, error) {
	now := time.Now()

	key, err := rsa.GenerateKey(rand.Reader, rsaKeySize)
	if err != nil {
		return CertPair{}, fmt.Errorf("unable to create private key: %v", err)
	}

	serial := new(big.Int).Set(s.nextSerial)
	s.nextSerial.Add(s.nextSerial, bigOne)

	template := x509.Certificate{
		Subject:      pkix.Name{CommonName: cfg.CommonName, Organization: cfg.Organization},
		DNSNames:     cfg.AltNames.DNSNames,
		IPAddresses:  cfg.AltNames.IPs,
		SerialNumber: serial,
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  cfg.Usages,
		NotBefore:    now.UTC(),
		NotAfter:     now.Add(expiry).UTC(),
	}

	certRaw, err := x509.CreateCertificate(rand.Reader, &template, s.caCert, key.Public(), s.caKey)
	if err != nil {
		return CertPair{}, fmt.Errorf("unable to create certificate: %v", err)
	}

	certificate, err := x509.ParseCertificate(certRaw)
	if err != nil {
		return CertPair{}, fmt.Errorf("generated invalid certificate, could not parse: %v", err)
	}

	return CertPair{
		Key:  key,
		Cert: certificate,
	}, nil
}

func (s selfSignedCASigner) CAData() []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: s.caCert.Raw,
	})
}

func (s selfSignedCASigner) GetSigner() crypto.Signer {
	return s.caKey
}

func (s selfSignedCASigner) CA() *openshiftcrypto.CA {
	return &openshiftcrypto.CA{
		Config: &openshiftcrypto.TLSCertificateConfig{
			Certs: []*x509.Certificate{s.caCert},
			Key:   s.caKey,
		},
		SerialGenerator: &openshiftcrypto.RandomSerialGenerator{},
	}
}

type CertPair struct {
	Key  crypto.Signer
	Cert *x509.Certificate
}

func (k CertPair) CertBytes() []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: k.Cert.Raw,
	})
}

func (k CertPair) AsBytes() (cert []byte, key []byte, err error) {
	cert = k.CertBytes()

	rawKeyData, err := x509.MarshalPKCS8PrivateKey(k.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to encode private key: %v", err)
	}

	key = pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: rawKeyData,
	})

	return cert, key, nil
}

const (
	TLSCACert = "ca.crt"
	TLSCAKey  = "ca.key"
)

func dumpCASecret(c kubernetes.Interface, namespace, name string, caCertData, caKeyData []byte) (bool, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			TLSCACert: caCertData,
			TLSCAKey:  caKeyData,
		},
	}
	_, err := c.CoreV1().Secrets(namespace).Create(context.TODO(), secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return true, nil
	}
	return false, err
}

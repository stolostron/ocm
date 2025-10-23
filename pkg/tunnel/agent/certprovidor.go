package agent

import (
	"crypto/x509"
	"os"
)

// CertificateProvider provides the root certificate pool for TLS connections
type CertificateProvider interface {
	GetRootCAs() (*x509.CertPool, error)
}

type CertificateProviderImplt struct{}

func (c CertificateProviderImplt) GetRootCAs() (*x509.CertPool, error) {
	const (
		rootCAFile = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	)
	rootCAs := x509.NewCertPool()

	// ca for accessing apiserver
	apiserverPem, err := os.ReadFile(rootCAFile)
	if err != nil {
		return nil, err
	}
	rootCAs.AppendCertsFromPEM(apiserverPem)

	// TODO:@xuezhaojun ca for accessing OCP service
	// openshift-service-ca.crt

	return rootCAs, nil
}

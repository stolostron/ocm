package registration_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/rand"
	"path"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	certificatesv1 "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonoptions "open-cluster-management.io/ocm/pkg/common/options"
	"open-cluster-management.io/ocm/pkg/registration/hub/user"
	registerfactory "open-cluster-management.io/ocm/pkg/registration/register/factory"
	"open-cluster-management.io/ocm/pkg/registration/spoke"
	"open-cluster-management.io/ocm/test/integration/util"
)

// setupTestCluster sets up a managed cluster and returns a cleanup function
func setupTestCluster(clusterName, secretName, dirSuffix string) func() {
	hubKubeconfigDir := path.Join(util.TestDir, dirSuffix, "hub-kubeconfig")

	agentOptions := &spoke.SpokeAgentOptions{
		BootstrapKubeconfig:      bootstrapKubeConfigFile,
		HubKubeconfigSecret:      secretName,
		ClusterHealthCheckPeriod: 1 * time.Minute,
		RegisterDriverOption:     registerfactory.NewOptions(),
	}

	commOptions := commonoptions.NewAgentOptions()
	commOptions.HubKubeconfigDir = hubKubeconfigDir
	commOptions.SpokeClusterName = clusterName

	cancel := runAgent(dirSuffix, agentOptions, commOptions, spokeCfg)

	// Wait for cluster and CSR creation
	gomega.Eventually(func() error {
		if _, err := util.GetManagedCluster(clusterClient, clusterName); err != nil {
			return err
		}
		return nil
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())

	gomega.Eventually(func() error {
		if _, err := util.FindUnapprovedSpokeCSR(kubeClient, clusterName); err != nil {
			return err
		}
		return nil
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())

	// Approve and accept
	err := authn.ApproveSpokeClusterCSR(kubeClient, clusterName, 24*time.Hour)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = util.AcceptManagedCluster(clusterClient, clusterName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Wait for kubeconfig
	gomega.Eventually(func() error {
		if _, err := util.GetFilledHubKubeConfigSecret(kubeClient, testNamespace, secretName); err != nil {
			return err
		}
		return nil
	}, eventuallyTimeout, eventuallyInterval).ShouldNot(gomega.HaveOccurred())

	return cancel
}

// verifyCSRRejected creates a malicious CSR and verifies it's NOT auto-approved
func verifyCSRRejected(clusterName, cn string, orgs []string) {
	maliciousCSR := createMaliciousCSR(clusterName, cn, orgs)

	_, err := kubeClient.CertificatesV1().CertificateSigningRequests().Create(
		context.Background(),
		maliciousCSR,
		metav1.CreateOptions{},
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify CSR remains unapproved
	gomega.Consistently(func() bool {
		csr, err := kubeClient.CertificatesV1().CertificateSigningRequests().Get(
			context.Background(),
			maliciousCSR.Name,
			metav1.GetOptions{},
		)
		if err != nil {
			return false
		}
		return len(csr.Status.Conditions) == 0
	}, 10*time.Second, 1*time.Second).Should(gomega.BeTrue(), "Malicious CSR should NOT be auto-approved")

	// Cleanup
	err = kubeClient.CertificatesV1().CertificateSigningRequests().Delete(
		context.Background(),
		maliciousCSR.Name,
		metav1.DeleteOptions{},
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.Describe("CSR Security Validation", func() {
	type csrSecurityTestCase struct {
		clusterName string
		secret      string
		dirSuffix   string
		cnCluster   string
		orgCluster  string
	}

	tests := []csrSecurityTestCase{
		{
			clusterName: "securitytest-cluster1",
			secret:      "securitytest-hub-kubeconfig-secret", //#nosec G101
			dirSuffix:   "securitytest",
			cnCluster:   "securitytest-cluster1",
			orgCluster:  "securitytest-cluster1xyz",
		},
		{
			clusterName: "securitytest-cluster2",
			secret:      "securitytest2-hub-kubeconfig-secret", //#nosec G101
			dirSuffix:   "securitytest2",
			cnCluster:   "securitytest-cluster2xyz",
			orgCluster:  "securitytest-cluster2xyz",
		},
	}

	for i, tc := range tests {
		tc := tc
		ginkgo.It(fmt.Sprintf("Should reject malicious CSR (case %d) where CN or org cluster name doesn't exactly match label", i+1), func() {
			cancel := setupTestCluster(tc.clusterName, tc.secret, tc.dirSuffix)
			defer cancel()

			verifyCSRRejected(
				tc.clusterName,
				user.SubjectPrefix+tc.cnCluster+":agent1",
				[]string{user.SubjectPrefix + tc.orgCluster, user.ManagedClustersGroup},
			)
		})
	}
})

// createMaliciousCSR creates a CSR with custom label, CN, and organizations for testing
func createMaliciousCSR(clusterName, commonName string, orgs []string) *certificatesv1.CertificateSigningRequest {
	insecureRand := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec
	pk, err := ecdsa.GenerateKey(elliptic.P256(), insecureRand)
	if err != nil {
		panic(err)
	}

	csrb, err := x509.CreateCertificateRequest(cryptorand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: orgs,
		},
	}, pk)
	if err != nil {
		panic(err)
	}

	csrName := fmt.Sprintf("malicious-csr-%d", time.Now().UnixNano())
	return &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: csrName,
			Labels: map[string]string{
				"open-cluster-management.io/cluster-name": clusterName,
			},
		},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Username: user.SubjectPrefix + clusterName + ":agent1",
			Request: pem.EncodeToMemory(&pem.Block{
				Type:  "CERTIFICATE REQUEST",
				Bytes: csrb,
			}),
			SignerName: certificatesv1.KubeAPIServerClientSignerName,
			Usages: []certificatesv1.KeyUsage{
				certificatesv1.UsageDigitalSignature,
				certificatesv1.UsageKeyEncipherment,
				certificatesv1.UsageClientAuth,
			},
		},
	}
}

package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// RequestProcessor processes HTTP requests before proxying them to the target service
type RequestProcessor interface {
	Process(targetHost string, r *http.Request) (error, int)
}

type RequestProcessorImplt struct {
	hubKubeClient            kubernetes.Interface
	managedClusterKubeClient kubernetes.Interface
}

// NewRequestProcessorImplt creates a new RequestProcessorImplt instance
func NewRequestProcessorImplt(hubKubeClient, managedClusterKubeClient kubernetes.Interface) *RequestProcessorImplt {
	return &RequestProcessorImplt{
		hubKubeClient:            hubKubeClient,
		managedClusterKubeClient: managedClusterKubeClient,
	}
}

func (p *RequestProcessorImplt) Process(targetHost string, r *http.Request) (error, int) {
	if targetHost != "kubernetes.default.svc" {
		return nil, http.StatusOK
	}

	return p.processAuthentication(r)
}

func (p *RequestProcessorImplt) processAuthentication(req *http.Request) (error, int) {
	token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")

	// determine if the token is a managed cluster user
	managedClusterAuthenticated, _, err := p.managedClusterUserAuthenticatedAndInfo(token)
	if err != nil {
		klog.ErrorS(err, "managed cluster authentication failed")
		return fmt.Errorf("managed cluster authentication failed: %v", err), http.StatusUnauthorized
	}

	if !managedClusterAuthenticated {
		// determine if the token is a hub user
		hubAuthenticated, hubUserInfo, err := p.hubUserAuthenticatedAndInfo(token)
		if err != nil {
			klog.ErrorS(err, "hub cluster authentication failed")
			return fmt.Errorf("authentication failed: managed cluster auth: not authenticated, hub cluster auth error: %v", err), http.StatusUnauthorized
		}
		if !hubAuthenticated {
			klog.ErrorS(err, "authentication failed: token is neither valid for managed cluster nor hub cluster")
			return fmt.Errorf("authentication failed: token is neither valid for managed cluster nor hub cluster"), http.StatusUnauthorized
		}

		if err := p.processHubUser(req, hubUserInfo); err != nil {
			klog.ErrorS(err, "failed to process hub user")
			return fmt.Errorf("failed to process hub user: %v", err), http.StatusUnauthorized
		}
	}

	return nil, http.StatusOK
}

func (p *RequestProcessorImplt) hubUserAuthenticatedAndInfo(token string) (bool, *authenticationv1.UserInfo, error) {
	tokenReview, err := p.hubKubeClient.AuthenticationV1().TokenReviews().Create(context.Background(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{
			Token: token,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, nil, err
	}

	if !tokenReview.Status.Authenticated {
		return false, nil, nil
	}
	return true, &tokenReview.Status.User, nil
}

func (p *RequestProcessorImplt) managedClusterUserAuthenticatedAndInfo(token string) (bool, *authenticationv1.UserInfo, error) {
	tokenReview, err := p.managedClusterKubeClient.AuthenticationV1().TokenReviews().Create(context.Background(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{
			Token: token,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, nil, err
	}

	if !tokenReview.Status.Authenticated {
		return false, nil, nil
	}
	return true, &tokenReview.Status.User, nil
}

// processHubUser handles the hub user specific operations including impersonation
func (p *RequestProcessorImplt) processHubUser(req *http.Request, hubUserInfo *authenticationv1.UserInfo) error {
	// set impersonate group header
	for _, group := range hubUserInfo.Groups {
		// Here using `Add` instead of `Set` to support multiple groups
		req.Header.Add("Impersonate-Group", group)
	}

	// check if the hub user is serviceaccount kind, if so, add "cluster:hub:" prefix to the username
	if strings.HasPrefix(hubUserInfo.Username, "system:serviceaccount:") {
		req.Header.Set("Impersonate-User", fmt.Sprintf("cluster:hub:%s", hubUserInfo.Username))
	} else {
		req.Header.Set("Impersonate-User", hubUserInfo.Username)
	}

	// replace the original token with cluster-proxy service-account token which has impersonate permission
	token, err := p.getImpersonateToken()
	if err != nil {
		return fmt.Errorf("failed to get impersonate token: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (p *RequestProcessorImplt) getImpersonateToken() (string, error) {
	// Read the latest token from the mounted file
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return "", err
	}
	return string(token), nil
}

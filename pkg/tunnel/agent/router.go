package agent

import (
	"fmt"
	"net/http"
	"strings"

	utilnet "k8s.io/apimachinery/pkg/util/net"
)

// Router handles request routing for both hub and agent sides.
// ---
// An example of service: https://<route location cluster-proxy>/<managed_cluster_name>/api/v1/namespaces/<namespace_name>/services/<[https:]service_name[:port_name]>/proxy-service/<service_path>
// Target proto: https
// Target host: <service_name>.<namespace_name>.svc:<port_name>
// Target path: /<service_path>
// ---
// An example of kube-apiserver: https://<route location cluster-proxy>/<managed_cluster_name>/api/pods?timeout=32s
// Target proto: https
// Target host: kubernetes.default.svc
// Target path: /<api_path>
// ---
// Note:
// 1. targetPath must not contain the query part, it should be stripped before returning.
// For example, /api/v1/pods?timeout=32s should be stripped to /api/v1/pods
type Router interface {
	// ParseTargetService retrieves target service information from the request (agent side)
	// Returns protocol, host, path, and error
	ParseTargetService(r *http.Request) (targetproto, targethost, targetpath string, err error)
}

type RouterImpl struct{}

const (
	ProxyTypeService = iota
	ProxyTypeKubeAPIServer
)

func getProxyType(pathParms []string) int {
	if len(pathParms) > 9 && pathParms[8] == "proxy-service" {
		return ProxyTypeService
	}
	return ProxyTypeKubeAPIServer
}

func (router *RouterImpl) ParseTargetService(r *http.Request) (targetproto, targethost, targetpath string, err error) {
	pathParams := strings.Split(r.URL.Path, "/")

	switch getProxyType(pathParams) {
	case ProxyTypeKubeAPIServer:
		// For kube-apiserver requests: /<cluster-name>/api/...
		// Target proto: https
		// Target host: kubernetes.default.svc
		// Target path: /<api_path> (remove cluster name from path)
		if len(pathParams) < 3 {
			return "", "", "", fmt.Errorf("invalid kube-apiserver request path: %s", r.RequestURI)
		}
		// Remove cluster name from path: /cluster-name/api/v1/pods -> /api/v1/pods
		targetPath := "/" + strings.Join(pathParams[2:], "/")
		return "https", "kubernetes.default.svc", targetPath, nil

	case ProxyTypeService:
		// For service requests: /<cluster-name>/api/v1/namespaces/<namespace>/services/<service>/proxy-service/<service_path>
		// Target proto: https
		// Target host: <service_name>.<namespace_name>.svc:<port_name>
		// Target path: /<service_path>
		if len(pathParams) < 10 {
			return "", "", "", fmt.Errorf("invalid service proxy request path: %s", r.RequestURI)
		}

		namespace := pathParams[5]
		proto, service, port, valid := utilnet.SplitSchemeNamePort(pathParams[7])
		if !valid {
			return "", "", "", fmt.Errorf("invalid service name: %s", pathParams[7])
		}
		if proto != "https" {
			return "", "", "", fmt.Errorf("for security reason, only https is supported:unsupported protocol: %s", proto)
		}

		// Extract service path: everything after proxy-service
		servicePath := "/" + strings.Join(pathParams[9:], "/")
		targetHost := fmt.Sprintf("%s.%s.svc:%s", service, namespace, port)

		return "https", targetHost, servicePath, nil

	default:
		return "", "", "", fmt.Errorf("unknown proxy type, please check your request path: %s", r.RequestURI)
	}
}

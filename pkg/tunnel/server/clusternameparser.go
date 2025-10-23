package server

import (
	"fmt"
	"net/http"
	"strings"
)

// ClusterNameParser defines the interface for parsing cluster names from HTTP requests
type ClusterNameParser interface {
	ParseClusterName(r *http.Request) (clusterName string, err error)
}

// clusterNameParserImplt implements the ClusterNameParser interface
type clusterNameParserImplt struct{}

// NewClusterNameParserImplt creates a new default cluster name parser implementation
func NewClusterNameParserImplt() ClusterNameParser {
	return &clusterNameParserImplt{}
}

// ParseClusterName parses the cluster name from the HTTP request URI
func (p *clusterNameParserImplt) ParseClusterName(r *http.Request) (clusterName string, err error) {
	urlparams := strings.Split(r.RequestURI, "/")
	if len(urlparams) < 2 {
		err = fmt.Errorf("requestURI format not correct, path less than 2: %s", r.RequestURI)
		return
	}
	return urlparams[1], nil
}

/*
Copyright (c) 2025, Oracle and/or its affiliates.

Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl.
*/

package platforms

import (
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/host"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platforms/openshift"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platforms/openstack"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/platforms/oraclepcac3"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/utils"
)

//go:generate ../../bin/mockgen -destination mock/mock_platforms.go -source platforms.go
type Interface interface {
	openshift.OpenshiftContextInterface
	openstack.OpenstackInterface
	oraclepcac3.OraclePcaC3Interface
}

type platformHelper struct {
	openshift.OpenshiftContextInterface
	openstack.OpenstackInterface
	oraclepcac3.OraclePcaC3Interface
}

func NewDefaultPlatformHelper() (Interface, error) {
	openshiftContext, err := openshift.New()
	if err != nil {
		return nil, err
	}
	utilsHelper := utils.New()
	hostManager, err := host.NewHostManager(utilsHelper)
	if err != nil {
		log.Log.Error(err, "failed to create host manager")
		return nil, err
	}
	openstackContext := openstack.New(hostManager)
	oraclePcaC3Context := oraclepcac3.New(hostManager)

	return &platformHelper{
		openshiftContext,
		openstackContext,
		oraclePcaC3Context,
	}, nil
}

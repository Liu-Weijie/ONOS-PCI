// SPDX-FileCopyrightText: 2020-present Open Networking Foundation <info@opennetworking.org>
//
// SPDX-License-Identifier: Apache-2.0

package scale

import (
	"github.com/onosproject/helmit/pkg/helm"
	"github.com/onosproject/helmit/pkg/input"
	"github.com/onosproject/helmit/pkg/test"
	"github.com/onosproject/onos-pci/test/utils"
	testutils "github.com/onosproject/onos-ric-sdk-go/pkg/utils"
)

// TestSuite is the primary onos-pci test suite
type TestSuite struct {
	sdran *helm.HelmRelease
	test.Suite
}

// SetupTestSuite sets up the onos-pci test suite
func (s *TestSuite) SetupTestSuite(c *input.Context) error {

	// write files
	err := utils.WriteFile("/tmp/tls.cacrt", utils.TLSCacrt)
	if err != nil {
		return err
	}
	err = utils.WriteFile("/tmp/tls.crt", utils.TLSCrt)
	if err != nil {
		return err
	}
	err = utils.WriteFile("/tmp/tls.key", utils.TLSKey)
	if err != nil {
		return err
	}
	err = utils.WriteFile("/tmp/config.json", utils.ConfigJSON)
	if err != nil {
		return err
	}

	sdran, err := utils.CreateSdranRelease(c)
	if err != nil {
		return err
	}
	s.sdran = sdran
	sdran.Set("ran-simulator.pci.metricName", "metrics").
		Set("ran-simulator.pci.modelName", "scale-50-150")
	r := sdran.Install(true)

	//logging.GetLogger("onos", "proxy", "e2", "v1beta1", "balancer").SetLevel(logging.DebugLevel)
	testutils.StartTestProxy()
	return r
}

// TearDownTestSuite uninstalls helm chart released
func (s *TestSuite) TearDownTestSuite() error {
	testutils.StopTestProxy()
	return s.sdran.Uninstall()
}

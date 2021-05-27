// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//nolint:dupl,goconst
package network_test

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/controller/runtime"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/cosi-project/runtime/pkg/state/impl/inmem"
	"github.com/cosi-project/runtime/pkg/state/impl/namespaced"
	"github.com/stretchr/testify/suite"
	"github.com/talos-systems/go-procfs/procfs"
	"github.com/talos-systems/go-retry/retry"
	"inet.af/netaddr"

	netctrl "github.com/talos-systems/talos/internal/app/machined/pkg/controllers/network"
	"github.com/talos-systems/talos/pkg/logging"
	"github.com/talos-systems/talos/pkg/machinery/config/types/v1alpha1"
	"github.com/talos-systems/talos/pkg/machinery/nethelpers"
	"github.com/talos-systems/talos/pkg/resources/config"
	"github.com/talos-systems/talos/pkg/resources/network"
)

type LinkConfigSuite struct {
	suite.Suite

	state state.State

	runtime *runtime.Runtime
	wg      sync.WaitGroup

	ctx       context.Context
	ctxCancel context.CancelFunc
}

func (suite *LinkConfigSuite) SetupTest() {
	suite.ctx, suite.ctxCancel = context.WithTimeout(context.Background(), 3*time.Minute)

	suite.state = state.WrapCore(namespaced.NewState(inmem.Build))

	var err error

	suite.runtime, err = runtime.NewRuntime(suite.state, logging.Wrap(log.Writer()))
	suite.Require().NoError(err)
}

func (suite *LinkConfigSuite) startRuntime() {
	suite.wg.Add(1)

	go func() {
		defer suite.wg.Done()

		suite.Assert().NoError(suite.runtime.Run(suite.ctx))
	}()
}

func (suite *LinkConfigSuite) assertLinks(requiredIDs []string, check func(*network.LinkSpec) error) error {
	missingIDs := make(map[string]struct{}, len(requiredIDs))

	for _, id := range requiredIDs {
		missingIDs[id] = struct{}{}
	}

	resources, err := suite.state.List(suite.ctx, resource.NewMetadata(network.ConfigNamespaceName, network.LinkSpecType, "", resource.VersionUndefined))
	if err != nil {
		return err
	}

	for _, res := range resources.Items {
		_, required := missingIDs[res.Metadata().ID()]
		if !required {
			continue
		}

		delete(missingIDs, res.Metadata().ID())

		if err = check(res.(*network.LinkSpec)); err != nil {
			return retry.ExpectedError(err)
		}
	}

	if len(missingIDs) > 0 {
		return retry.ExpectedError(fmt.Errorf("some resources are missing: %q", missingIDs))
	}

	return nil
}

func (suite *LinkConfigSuite) TestLoopback() {
	suite.Require().NoError(suite.runtime.RegisterController(&netctrl.LinkConfigController{}))

	suite.startRuntime()

	suite.Assert().NoError(retry.Constant(3*time.Second, retry.WithUnits(100*time.Millisecond)).Retry(
		func() error {
			return suite.assertLinks([]string{
				"default/lo",
			}, func(r *network.LinkSpec) error {
				suite.Assert().Equal("lo", r.Status().Name)
				suite.Assert().True(r.Status().Up)
				suite.Assert().False(r.Status().Logical)
				suite.Assert().Equal(network.ConfigDefault, r.Status().ConfigLayer)

				return nil
			})
		}))
}

func (suite *LinkConfigSuite) TestCmdline() {
	suite.Require().NoError(suite.runtime.RegisterController(&netctrl.LinkConfigController{
		Cmdline: procfs.NewCmdline("ip=172.20.0.2::172.20.0.1:255.255.255.0::eth1:::::"),
	}))

	suite.startRuntime()

	suite.Assert().NoError(retry.Constant(3*time.Second, retry.WithUnits(100*time.Millisecond)).Retry(
		func() error {
			return suite.assertLinks([]string{
				"cmdline/eth1",
			}, func(r *network.LinkSpec) error {
				suite.Assert().Equal("eth1", r.Status().Name)
				suite.Assert().True(r.Status().Up)
				suite.Assert().False(r.Status().Logical)
				suite.Assert().Equal(network.ConfigCmdline, r.Status().ConfigLayer)

				return nil
			})
		}))
}

func (suite *LinkConfigSuite) TestMachineConfiguration() {
	suite.Require().NoError(suite.runtime.RegisterController(&netctrl.LinkConfigController{}))

	suite.startRuntime()

	u, err := url.Parse("https://foo:6443")
	suite.Require().NoError(err)

	cfg := config.NewMachineConfig(&v1alpha1.Config{
		ConfigVersion: "v1alpha1",
		MachineConfig: &v1alpha1.MachineConfig{
			MachineNetwork: &v1alpha1.NetworkConfig{
				NetworkInterfaces: []*v1alpha1.Device{
					{
						DeviceInterface: "eth0",
						DeviceVlans: []*v1alpha1.Vlan{
							{
								VlanID:   24,
								VlanCIDR: "10.0.0.1/8",
							},
							{
								VlanID:   48,
								VlanCIDR: "10.0.0.2/8",
							},
						},
					},
					{
						DeviceInterface: "eth1",
						DeviceCIDR:      "192.168.0.24/28",
					},
					{
						DeviceIgnore:    true,
						DeviceInterface: "eth2",
						DeviceCIDR:      "192.168.0.24/28",
					},
					{
						DeviceInterface: "eth2",
					},
					{
						DeviceInterface: "eth3",
					},
					{
						DeviceInterface: "bond0",
						DeviceBond: &v1alpha1.Bond{
							BondInterfaces: []string{"eth2", "eth3"},
							BondMode:       "balance-xor",
						},
					},
					{
						DeviceInterface: "dummy0",
						DeviceDummy:     true,
					},
					{
						DeviceInterface: "wireguard0",
						DeviceWireguardConfig: &v1alpha1.DeviceWireguardConfig{
							WireguardPrivateKey: "ABC",
							WireguardPeers: []*v1alpha1.DeviceWireguardPeer{
								{
									WireguardPublicKey: "DEF",
									WireguardEndpoint:  "10.0.0.1:3000",
									WireguardAllowedIPs: []string{
										"10.2.3.0/24",
										"10.2.4.0/24",
									},
								},
							},
						},
					},
				},
			},
		},
		ClusterConfig: &v1alpha1.ClusterConfig{
			ControlPlane: &v1alpha1.ControlPlaneConfig{
				Endpoint: &v1alpha1.Endpoint{
					URL: u,
				},
			},
		},
	})

	suite.Require().NoError(suite.state.Create(suite.ctx, cfg))

	suite.Assert().NoError(retry.Constant(3*time.Second, retry.WithUnits(100*time.Millisecond)).Retry(
		func() error {
			return suite.assertLinks([]string{
				"configuration/eth0",
				"configuration/eth0.24",
				"configuration/eth0.48",
				"configuration/eth1",
				"configuration/eth2",
				"configuration/eth3",
				"configuration/bond0",
				"configuration/dummy0",
				"configuration/wireguard0",
			}, func(r *network.LinkSpec) error {
				suite.Assert().Equal(network.ConfigMachineConfiguration, r.Status().ConfigLayer)

				switch r.Status().Name {
				case "eth0", "eth1":
					suite.Assert().True(r.Status().Up)
					suite.Assert().False(r.Status().Logical)
				case "eth0.24", "eth0.48":
					suite.Assert().True(r.Status().Up)
					suite.Assert().True(r.Status().Logical)
					suite.Assert().Equal(nethelpers.LinkEther, r.Status().Type)
					suite.Assert().Equal(network.LinkKindVLAN, r.Status().Kind)
					suite.Assert().Equal("eth0", r.Status().ParentName)
					suite.Assert().Equal(nethelpers.VLANProtocol8021Q, r.Status().VLAN.Protocol)

					if r.Status().Name == "eth0.24" {
						suite.Assert().EqualValues(24, r.Status().VLAN.VID)
					} else {
						suite.Assert().EqualValues(48, r.Status().VLAN.VID)
					}
				case "eth2", "eth3":
					suite.Assert().False(r.Status().Up)
					suite.Assert().False(r.Status().Logical)
					suite.Assert().Equal("bond0", r.Status().MasterName)
				case "bond0":
					suite.Assert().True(r.Status().Up)
					suite.Assert().True(r.Status().Logical)
					suite.Assert().Equal(nethelpers.LinkEther, r.Status().Type)
					suite.Assert().Equal(network.LinkKindBond, r.Status().Kind)
					suite.Assert().Equal(nethelpers.BondModeXOR, r.Status().BondMaster.Mode)
					suite.Assert().True(r.Status().BondMaster.UseCarrier)
				case "wireguard0":
					suite.Assert().True(r.Status().Up)
					suite.Assert().True(r.Status().Logical)
					suite.Assert().Equal(nethelpers.LinkNone, r.Status().Type)
					suite.Assert().Equal(network.LinkKindWireguard, r.Status().Kind)
					suite.Assert().Equal(network.WireguardSpec{
						PrivateKey: "ABC",
						Peers: []network.WireguardPeer{
							{
								PublicKey: "DEF",
								Endpoint:  "10.0.0.1:3000",
								AllowedIPs: []netaddr.IPPrefix{
									netaddr.MustParseIPPrefix("10.2.3.0/24"),
									netaddr.MustParseIPPrefix("10.2.4.0/24"),
								},
							},
						},
					}, r.Status().Wireguard)
				}

				return nil
			})
		}))
}

func TestLinkConfigSuite(t *testing.T) {
	suite.Run(t, new(LinkConfigSuite))
}
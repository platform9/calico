// Copyright (c) 2023 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build fvtests

package fv_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	api "github.com/projectcalico/api/pkg/apis/projectcalico/v3"
	"github.com/sirupsen/logrus"

	"github.com/projectcalico/calico/felix/fv/connectivity"
	"github.com/projectcalico/calico/felix/fv/containers"
	"github.com/projectcalico/calico/felix/fv/infrastructure"
	"github.com/projectcalico/calico/felix/fv/workload"
	client "github.com/projectcalico/calico/libcalico-go/lib/clientv3"
	"github.com/projectcalico/calico/libcalico-go/lib/options"
)

var _ = Context("_BPF-SAFE_ BPF policy scale tests", func() {
	if !BPFMode() {
		// Non-BPF run.
		return
	}

	var (
		etcd     *containers.Container
		tc       infrastructure.TopologyContainers
		felixPID int
		client   client.Interface
		infra    infrastructure.DatastoreInfra
		w        [2]*workload.Workload
		cc       *connectivity.Checker
	)

	BeforeEach(func() {
		topologyOptions := infrastructure.DefaultTopologyOptions()
		topologyOptions.FelixLogSeverity = "Info"
		topologyOptions.EnableIPv6 = false
		topologyOptions.ExtraEnvVars["FELIX_BPFLogLevel"] = "off"
		topologyOptions.ExtraEnvVars["FELIX_BPFMapSizeIPSets"] = "10000000"
		logrus.SetLevel(logrus.InfoLevel)
		tc, etcd, client, infra = infrastructure.StartSingleNodeEtcdTopology(topologyOptions)
		felixPID = tc.Felixes[0].GetFelixPID()
		_ = felixPID
		w[0] = workload.Run(tc.Felixes[0], "w0", "default", "10.65.0.2", "8085", "tcp")
		w[1] = workload.Run(tc.Felixes[0], "w1", "default", "10.65.0.3", "8085", "tcp")
		cc = &connectivity.Checker{}
	})

	AfterEach(func() {
		for _, wl := range w {
			wl.Stop()
		}
		tc.Stop()

		if CurrentGinkgoTestDescription().Failed {
			etcd.Exec("etcdctl", "get", "/", "--prefix", "--keys-only")
		}
		etcd.Stop()
		infra.Stop()
	})

	It("should handle thousands of policy rules", func() {
		// This test activates thousands of rules on one endpoint, which
		// requires the policy program to be split into sub-programs.

		// 12500 rules
		const (
			numPols        = 250
			numRulesPerPol = 50
			numSets        = numPols * numRulesPerPol
		)
		createNetworkSetPolicies(client, numPols, numRulesPerPol)

		By("Creating a workload, activating the policies")
		// Create a workload that uses the policy.
		w[0].ConfigureInInfra(infra)
		w[1].ConfigureInInfra(infra)

		Eventually(tc.Felixes[0].BPFNumPolProgramsFn(w[0].InterfaceName, "ingress"), "240s", "1s").Should(
			BeNumerically(">", 5))
		Eventually(tc.Felixes[0].BPFNumPolProgramsFn(w[1].InterfaceName, "ingress"), "20s", "1s").Should(
			BeNumerically(">", 5))

		cc.ExpectNone(w[0], w[1])
		cc.ExpectNone(w[1], w[0])
		cc.CheckConnectivityWithTimeout(30 * time.Second)

		cc.ResetExpectations()
		ns := api.NewGlobalNetworkSet()
		ns.Name = "netset-extra"
		ns.Labels = map[string]string{
			"netset": fmt.Sprintf("netset-%d", numSets-42),
		}
		ns.Spec.Nets = []string{w[0].IPNet()}
		_, err := client.GlobalNetworkSets().Create(context.TODO(), ns, options.SetOptions{})
		Expect(err).NotTo(HaveOccurred())

		cc.ExpectSome(w[0], w[1])
		cc.ExpectNone(w[1], w[0])
		cc.CheckConnectivityWithTimeout(30 * time.Second)
	})
})

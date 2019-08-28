// Copyright (c) 2018 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package restorer_test

import (
	"context"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/coreos/etcd/embed"
	"github.com/gardener/etcd-backup-restore/pkg/etcdutil"
	"github.com/gardener/etcd-backup-restore/pkg/snapshot/snapshotter"
	"github.com/gardener/etcd-backup-restore/pkg/snapstore"
	"github.com/gardener/etcd-backup-restore/test/utils"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const (
	outputDir    = "../../../test/output"
	etcdDir      = outputDir + "/default.etcd"
	snapstoreDir = outputDir + "/snapshotter.bkp"
	etcdEndpoint = "http://localhost:2379"
)

var (
	testCtx   = context.Background()
	logger    = logrus.New().WithField("suite", "restorer")
	etcd      *embed.Etcd
	err       error
	keyTo     int
	endpoints = []string{etcdEndpoint}
)

func TestRestorer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Restorer Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	var (
		data []byte
	)

	err = os.RemoveAll(outputDir)
	Expect(err).ShouldNot(HaveOccurred())

	etcd, err = utils.StartEmbeddedEtcd(testCtx, etcdDir, logger)
	Expect(err).ShouldNot(HaveOccurred())
	defer func() {
		etcd.Server.Stop()
		etcd.Close()
	}()

	populatorCtx, cancelPopulator := context.WithTimeout(testCtx, 15*time.Second)
	defer cancelPopulator()
	resp := &utils.EtcdDataPopulationResponse{}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go utils.PopulateEtcdWithWaitGroup(populatorCtx, wg, logger, endpoints, resp)
	deltaSnapshotPeriod := 1
	ctx := utils.ContextWithWaitGroupFollwedByGracePeriod(populatorCtx, wg, time.Duration(deltaSnapshotPeriod+2)*time.Second)
	err = runSnapshotter(logger, deltaSnapshotPeriod, endpoints, ctx.Done(), true)
	Expect(err).ShouldNot(HaveOccurred())

	keyTo = resp.KeyTo
	return data

}, func(data []byte) {})

var _ = SynchronizedAfterSuite(func() {}, cleanUp)

func cleanUp() {
	err = os.RemoveAll(etcdDir)
	Expect(err).ShouldNot(HaveOccurred())

	err = os.RemoveAll(snapstoreDir)
	Expect(err).ShouldNot(HaveOccurred())

	//for the negative scenario for invalid restoredir set to "" we need to cleanup the member folder in the working directory
	restoreDir := path.Clean("")
	err = os.RemoveAll(path.Join(restoreDir, "member"))
	Expect(err).ShouldNot(HaveOccurred())

}

// runSnapshotter creates a snapshotter object and runs it for a duration specified by 'snapshotterDurationSeconds'
func runSnapshotter(logger *logrus.Entry, deltaSnapshotPeriod int, endpoints []string, stopCh <-chan struct{}, startWithFullSnapshot bool) error {
	var (
		store                          snapstore.SnapStore
		certFile                       string
		keyFile                        string
		caFile                         string
		insecureTransport              bool
		insecureSkipVerify             bool
		maxBackups                     = 1
		etcdConnectionTimeout          = time.Duration(10)
		garbageCollectionPeriodSeconds = time.Duration(60)
		schedule                       = "0 0 1 1 *"
		garbageCollectionPolicy        = snapshotter.GarbageCollectionPolicyLimitBased
		etcdUsername                   string
		etcdPassword                   string
	)

	store, err = snapstore.GetSnapstore(&snapstore.Config{Container: snapstoreDir, Provider: "Local"})
	if err != nil {
		return err
	}

	tlsConfig := etcdutil.NewTLSConfig(
		certFile,
		keyFile,
		caFile,
		insecureTransport,
		insecureSkipVerify,
		endpoints,
		etcdUsername,
		etcdPassword,
	)

	snapshotterConfig, err := snapshotter.NewSnapshotterConfig(
		schedule,
		store,
		maxBackups,
		deltaSnapshotPeriod,
		snapshotter.DefaultDeltaSnapMemoryLimit,
		etcdConnectionTimeout,
		garbageCollectionPeriodSeconds,
		garbageCollectionPolicy,
		tlsConfig,
	)
	if err != nil {
		return err
	}

	ssr := snapshotter.NewSnapshotter(
		logger,
		snapshotterConfig,
	)

	return ssr.Run(stopCh, startWithFullSnapshot)
}

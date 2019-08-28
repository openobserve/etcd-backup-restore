// Copyright (c) 2019 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file.
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

package utils

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/embed"
	"github.com/sirupsen/logrus"
)

const (
	// KeyPrefix is prefix for keys inserted in etcd as a part of etcd-backup-restore tests.
	KeyPrefix = "/etcdbr/test/key-"
	// ValuePrefix is prefix for value inserted in etcd as a part of etcd-backup-restore tests.
	ValuePrefix = "val-"
)

// StartEmbeddedEtcd starts the embedded etcd for test purpose with minimal configuration.
func StartEmbeddedEtcd(ctx context.Context, etcdDir string, logger *logrus.Entry) (*embed.Etcd, error) {
	logger.Infoln("Starting embedded etcd...")
	cfg := embed.NewConfig()
	cfg.Dir = etcdDir
	cfg.EnableV2 = false
	cfg.Debug = false
	cfg.GRPCKeepAliveTimeout = 0
	cfg.SnapCount = 10
	DefaultListenPeerURLs := "http://localhost:0"
	DefaultListenClientURLs := "http://localhost:0"
	DefaultInitialAdvertisePeerURLs := "http://localhost:0"
	DefaultAdvertiseClientURLs := "http://localhost:0"
	lpurl, _ := url.Parse(DefaultListenPeerURLs)
	apurl, _ := url.Parse(DefaultInitialAdvertisePeerURLs)
	lcurl, _ := url.Parse(DefaultListenClientURLs)
	acurl, _ := url.Parse(DefaultAdvertiseClientURLs)
	cfg.LPUrls = []url.URL{*lpurl}
	cfg.LCUrls = []url.URL{*lcurl}
	cfg.APUrls = []url.URL{*apurl}
	cfg.ACUrls = []url.URL{*acurl}
	cfg.InitialCluster = cfg.InitialClusterFromName(cfg.Name)
	e, err := embed.StartEtcd(cfg)
	if err != nil {
		return nil, err
	}

	etcdWaitCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	select {
	case <-e.Server.ReadyNotify():
		logger.Infof("Embedded server is ready to listen client at: %s", e.Clients[0])
	case <-etcdWaitCtx.Done():
		e.Server.Stop() // trigger a shutdown
		e.Close()
		return nil, fmt.Errorf("server took too long to start")
	}
	return e, nil
}

// EtcdDataPopulationResponse is response about etcd data population
type EtcdDataPopulationResponse struct {
	KeyTo       int
	EndRevision int64
	Err         error
}

// PopulateEtcd sequentially puts key-value pairs into the embedded etcd, until stopped
func PopulateEtcd(ctx context.Context, logger *logrus.Entry, endpoints []string, keyFrom, keyTo int, response *EtcdDataPopulationResponse) {
	if response == nil {
		response = &EtcdDataPopulationResponse{}
	}
	response.KeyTo = keyFrom - 1
	logger.Infof("\n\nkeyFrom: %v, keyTo: %v", keyFrom, keyTo)
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		response.Err = fmt.Errorf("unable to start etcd client: %v", err)
		return
	}
	defer cli.Close()

	for {
		select {
		case <-ctx.Done():
			logger.Infof("Populated data till key %s into embedded etcd with etcd end revision :%v", KeyPrefix+strconv.Itoa(response.KeyTo), response.EndRevision)
			return
		case <-time.After(time.Second):
			if response.KeyTo > keyTo {
				logger.Infof("Populated data till key %s into embedded etcd with etcd end revision :%v", KeyPrefix+strconv.Itoa(response.KeyTo), response.EndRevision)
				return
			}
			key := KeyPrefix + strconv.Itoa(response.KeyTo)
			value := ValuePrefix + strconv.Itoa(response.KeyTo)
			resp, err := cli.Put(ctx, key, value)
			if err != nil {
				response.Err = fmt.Errorf("unable to put key-value pair (%s, %s) into embedded etcd: %v", key, value, err)
				logger.Infof("Populated data till key %s into embedded etcd with etcd end revision :%v", KeyPrefix+strconv.Itoa(response.KeyTo), response.EndRevision)
				return
			}
			response.KeyTo++
			response.EndRevision = resp.Header.GetRevision()
			//call a delete for every 10th Key after putting it in the store to check deletes in consistency check
			// handles deleted keys as every 10th key is deleted during populate etcd call
			// this handling is also done in the checkDataConsistency() in restorer_test.go file
			// also it assumes that the deltaSnapshotDuration is more than 10.
			// if you change the constant please change the factor accordingly to have coverage of delete scenarios.
			if math.Mod(float64(response.KeyTo), 10) == 0 {
				resp, err := cli.Delete(ctx, key)
				if err != nil {
					response.Err = fmt.Errorf("unable to delete key (%s) from embedded etcd: %v", key, err)
					logger.Infof("Populated data till key %s into embedded etcd with etcd end revision :%v", KeyPrefix+strconv.Itoa(response.KeyTo), response.EndRevision)
					return
				}
				response.EndRevision = resp.Header.GetRevision()
			}
		}
	}
}

// PopulateEtcdWithWaitGroup sequentially puts key-value pairs into the embedded etcd, until stopped via context.
func PopulateEtcdWithWaitGroup(ctx context.Context, wg *sync.WaitGroup, logger *logrus.Entry, endpoints []string, resp *EtcdDataPopulationResponse) {
	defer wg.Done()
	PopulateEtcd(ctx, logger, endpoints, 0, math.MaxInt64, resp)
}

// ContextWithWaitGroup returns a copy of parent with a new Done channel. The returned
// context's Done channel is closed when the the passed waitGroup's Wait function is called
// or when the parent context's Done channel is closed, whichever happens first.
func ContextWithWaitGroup(parent context.Context, wg *sync.WaitGroup) context.Context {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		wg.Wait()
		cancel()
	}()
	return ctx
}

// ContextWithGracePeriod returns a new context, whose Done channel is closed when parent
//context is closed with additional <gracePeriod>.
func ContextWithGracePeriod(parent context.Context, gracePeriod time.Duration) context.Context {
	ctx, cancel := context.WithCancel(context.TODO())
	go func() {
		<-parent.Done()
		<-time.After(gracePeriod)
		cancel()
	}()
	return ctx
}

// ContextWithWaitGroupFollwedByGracePeriod returns a new context, whose Done channel is closed
// when parent context is closed or wait of waitGroup is over, with additional <gracePeriod>.
func ContextWithWaitGroupFollwedByGracePeriod(parent context.Context, wg *sync.WaitGroup, gracePeriod time.Duration) context.Context {
	ctx := ContextWithWaitGroup(parent, wg)
	return ContextWithGracePeriod(ctx, gracePeriod)
}
